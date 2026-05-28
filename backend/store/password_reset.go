// File overview: Password reset token and small system setting persistence.

package store

import (
	"context"
	"os"
	"time"
)

func (s *Store) GetSystemSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM system_settings WHERE key = ?`, key).Scan(&value)
	return value, err
}

func (s *Store) SetSystemSetting(ctx context.Context, key, value string) error {
	ts := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO system_settings (key, value, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, ts, ts)
	return err
}

func (s *Store) CreatePasswordResetToken(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	if userID == 0 || tokenHash == "" {
		return ErrNotFound
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)`, userID, tokenHash, expiresAt.UTC().Unix(), nowUnix())
	return err
}

func (s *Store) UsePasswordResetToken(ctx context.Context, tokenHash, passwordHash string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	rollback := func() (User, error) {
		_ = tx.Rollback()
		return User{}, err
	}
	var userID int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM password_reset_tokens WHERE token_hash = ? AND used_at = 0 AND expires_at > ?`, tokenHash, nowUnix()).Scan(&userID)
	if err != nil {
		return rollback()
	}
	ts := nowUnix()
	if _, err = tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, ts, userID); err != nil {
		return rollback()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET used_at = ? WHERE token_hash = ?`, ts, tokenHash); err != nil {
		return rollback()
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return rollback()
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, userID)
}

func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if s.split {
		s.mu.Lock()
		us := s.userStores[userID]
		delete(s.userStores, userID)
		s.mu.Unlock()
		if us != nil {
			_ = us.Close()
		}
		if dir := s.UserDataDir(userID); dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}
