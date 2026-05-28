// File overview: System-scope password-reset profile and token storage.

package store

import "context"

func systemPasswordResetMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "system",
		Version: SystemSchemaVersion004,
		Label:   "system schema 004 password reset",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS system_settings (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS password_reset_tokens (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				token_hash TEXT NOT NULL UNIQUE,
				expires_at INTEGER NOT NULL,
				used_at INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_hash ON password_reset_tokens(token_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user ON password_reset_tokens(user_id, expires_at)`,
		},
		After: []migrationStep{
			{Label: "add user backup email", Run: ensureUserBackupEmailColumn},
		},
	}
}

func ensureUserBackupEmailColumn(ctx context.Context, s *Store) error {
	exists, err := tableColumnExists(ctx, s, "users", "backup_email")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN backup_email TEXT NOT NULL DEFAULT ''`)
	return err
}
