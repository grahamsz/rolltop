package schema

import (
	"context"
	"database/sql"

	"rolltop/backend/plugins"
)

// Migrations returns user-owned schema changes for PGP keys, PGP message flags,
// and Autocrypt identity settings.
func Migrations() []plugins.Migration {
	return []plugins.Migration{
		{
			Scope:    plugins.ScopeUser,
			PluginID: plugins.ClientSidePGP,
			ID:       "001_create_key_tables",
			Statements: []string{
				`CREATE TABLE IF NOT EXISTS identity_pgp_private_keys (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					identity_id INTEGER NOT NULL REFERENCES mail_identities(id) ON DELETE CASCADE,
					label TEXT NOT NULL DEFAULT '',
					fingerprint TEXT NOT NULL DEFAULT '',
					key_id TEXT NOT NULL DEFAULT '',
					user_ids TEXT NOT NULL DEFAULT '',
					public_key_armored TEXT NOT NULL DEFAULT '',
					encrypted_private_key TEXT NOT NULL DEFAULT '',
					private_key_storage TEXT NOT NULL DEFAULT 'server',
					revocation_certificate TEXT NOT NULL DEFAULT '',
					is_active_signing INTEGER NOT NULL DEFAULT 0,
					is_active_encryption INTEGER NOT NULL DEFAULT 0,
					is_decrypt_only INTEGER NOT NULL DEFAULT 0,
					created_at INTEGER NOT NULL,
					updated_at INTEGER NOT NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_identity_pgp_private_keys_user_identity ON identity_pgp_private_keys(user_id, identity_id)`,
				`CREATE TABLE IF NOT EXISTS contact_pgp_public_keys (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
					email TEXT NOT NULL DEFAULT '',
					normalized_email TEXT NOT NULL DEFAULT '',
					label TEXT NOT NULL DEFAULT '',
					fingerprint TEXT NOT NULL DEFAULT '',
					key_id TEXT NOT NULL DEFAULT '',
					user_ids TEXT NOT NULL DEFAULT '',
					public_key_armored TEXT NOT NULL DEFAULT '',
					is_preferred INTEGER NOT NULL DEFAULT 0,
					created_at INTEGER NOT NULL,
					updated_at INTEGER NOT NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_contact_pgp_public_keys_user_contact ON contact_pgp_public_keys(user_id, contact_id)`,
				`CREATE INDEX IF NOT EXISTS idx_contact_pgp_public_keys_user_email ON contact_pgp_public_keys(user_id, normalized_email, is_preferred DESC)`,
			},
		},
		{
			Scope:    plugins.ScopeUser,
			PluginID: plugins.ClientSidePGP,
			ID:       "002_message_flags",
			Apply: func(ctx context.Context, tx *sql.Tx) error {
				if err := ensureColumn(ctx, tx, "messages", "is_encrypted", `ALTER TABLE messages ADD COLUMN is_encrypted INTEGER NOT NULL DEFAULT 0`); err != nil {
					return err
				}
				if err := ensureColumn(ctx, tx, "messages", "is_signed", `ALTER TABLE messages ADD COLUMN is_signed INTEGER NOT NULL DEFAULT 0`); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_user_encrypted ON messages(user_id, is_encrypted)`); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_user_signed ON messages(user_id, is_signed)`)
				return err
			},
		},
		{
			Scope:    plugins.ScopeUser,
			PluginID: plugins.ClientSidePGP,
			ID:       "003_identity_autocrypt_setting",
			Apply: func(ctx context.Context, tx *sql.Tx) error {
				return ensureColumn(ctx, tx, "mail_identities", "autocrypt_enabled", `ALTER TABLE mail_identities ADD COLUMN autocrypt_enabled INTEGER NOT NULL DEFAULT 1`)
			},
		},
		{
			Scope:    plugins.ScopeUser,
			PluginID: plugins.ClientSidePGP,
			ID:       "004_private_key_storage",
			Apply: func(ctx context.Context, tx *sql.Tx) error {
				return ensureColumn(ctx, tx, "identity_pgp_private_keys", "private_key_storage", `ALTER TABLE identity_pgp_private_keys ADD COLUMN private_key_storage TEXT NOT NULL DEFAULT 'server'`)
			},
		},
		{
			Scope:    plugins.ScopeUser,
			PluginID: plugins.ClientSidePGP,
			ID:       "005_contact_key_source",
			Apply: func(ctx context.Context, tx *sql.Tx) error {
				if err := ensureColumn(ctx, tx, "contact_pgp_public_keys", "source_kind", `ALTER TABLE contact_pgp_public_keys ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'manual'`); err != nil {
					return err
				}
				return ensureColumn(ctx, tx, "contact_pgp_public_keys", "source_detail", `ALTER TABLE contact_pgp_public_keys ADD COLUMN source_detail TEXT NOT NULL DEFAULT ''`)
			},
		},
	}
}

func ensureColumn(ctx context.Context, tx *sql.Tx, table, column, ddl string) error {
	exists, err := columnExists(ctx, tx, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.ExecContext(ctx, ddl)
	return err
}

func columnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
