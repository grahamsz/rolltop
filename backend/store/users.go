// File overview: SQLite persistence layer for tenant-scoped MailMirror data.

package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

func cleanEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash string, isAdmin bool) (User, error) {
	email = cleanEmail(email)
	name = strings.TrimSpace(name)
	if email == "" || name == "" || passwordHash == "" {
		return User{}, errors.New("email, name, and password hash are required")
	}
	ts := nowUnix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (email, name, password_hash, is_admin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, email, name, passwordHash, boolInt(isAdmin), ts, ts)
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, id)
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	var u User
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, is_admin, date_locale, date_format, theme, created_at, updated_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &u.Theme, &created, &updated)
	u.CreatedAt = unixTime(created)
	u.UpdatedAt = unixTime(updated)
	u.DateFormat = normalizeUserDateFormat(u.DateFormat)
	u.Theme = normalizeUserTheme(u.Theme)
	return u, err
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, is_admin, date_locale, date_format, theme, created_at, updated_at FROM users WHERE email = ?`, cleanEmail(email)).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &u.Theme, &created, &updated)
	u.CreatedAt = unixTime(created)
	u.UpdatedAt = unixTime(updated)
	u.DateFormat = normalizeUserDateFormat(u.DateFormat)
	u.Theme = normalizeUserTheme(u.Theme)
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, name, password_hash, is_admin, date_locale, date_format, theme, created_at, updated_at FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var created, updated int64
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &u.Theme, &created, &updated); err != nil {
			return nil, err
		}
		u.CreatedAt = unixTime(created)
		u.UpdatedAt = unixTime(updated)
		u.DateFormat = normalizeUserDateFormat(u.DateFormat)
		u.Theme = normalizeUserTheme(u.Theme)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUserDisplayPreferences(ctx context.Context, userID int64, dateLocale, dateFormat, theme string) (User, error) {
	dateLocale = strings.TrimSpace(dateLocale)
	if len(dateLocale) > 64 {
		dateLocale = dateLocale[:64]
	}
	dateFormat = normalizeUserDateFormat(dateFormat)
	theme = normalizeUserTheme(theme)
	_, err := s.db.ExecContext(ctx, `UPDATE users SET date_locale = ?, date_format = ?, theme = ?, updated_at = ? WHERE id = ?`,
		dateLocale, dateFormat, theme, nowUnix(), userID)
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, userID)
}

func normalizeUserDateFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "locale", "dmy", "ymd":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "mdy"
	}
}

func normalizeUserTheme(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "classic_dark", "classic-dark":
		return "classic_dark"
	case "matrix", "modern":
		return "matrix"
	default:
		return "classic"
	}
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) (Session, error) {
	ts := nowUnix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions (user_id, token_hash, expires_at, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)`, userID, tokenHash, expiresAt.UTC().Unix(), ts, ts)
	if err != nil {
		return Session{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Session{}, err
	}
	return Session{ID: id, UserID: userID, TokenHash: tokenHash, ExpiresAt: expiresAt.UTC(), CreatedAt: unixTime(ts), LastSeenAt: unixTime(ts)}, nil
}

func (s *Store) GetSessionUser(ctx context.Context, tokenHash string) (Session, User, error) {
	var sess Session
	var u User
	var expires, created, lastSeen, userCreated, userUpdated int64
	err := s.db.QueryRowContext(ctx, `SELECT
			s.id, s.user_id, s.token_hash, s.expires_at, s.created_at, s.last_seen_at,
				u.id, u.email, u.name, u.password_hash, u.is_admin, u.date_locale, u.date_format, u.theme, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, nowUnix()).
		Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &expires, &created, &lastSeen,
			&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &u.Theme, &userCreated, &userUpdated)
	if err != nil {
		return Session{}, User{}, err
	}
	sess.ExpiresAt = unixTime(expires)
	sess.CreatedAt = unixTime(created)
	sess.LastSeenAt = unixTime(lastSeen)
	u.CreatedAt = unixTime(userCreated)
	u.UpdatedAt = unixTime(userUpdated)
	u.DateFormat = normalizeUserDateFormat(u.DateFormat)
	u.Theme = normalizeUserTheme(u.Theme)
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, nowUnix(), sess.ID)
	return sess, u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, nowUnix())
	return err
}
