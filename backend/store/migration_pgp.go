// File overview: PGP key storage and message PGP metadata schema migration.

package store

func userPGPMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion008,
		Label:   "user schema 008 pgp keys",
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
			`ALTER TABLE messages ADD COLUMN is_encrypted INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE messages ADD COLUMN is_signed INTEGER NOT NULL DEFAULT 0`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_encrypted ON messages(user_id, is_encrypted)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_signed ON messages(user_id, is_signed)`,
		},
	}
}
