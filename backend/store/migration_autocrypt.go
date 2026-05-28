// File overview: Per-identity Autocrypt key-discovery preference.

package store

import "context"

func userAutocryptMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion009,
		Label:   "user schema 009 autocrypt identity setting",
		After: []migrationStep{
			{Label: "add identity autocrypt setting", Run: ensureIdentityAutocryptColumn},
		},
	}
}

func ensureIdentityAutocryptColumn(ctx context.Context, s *Store) error {
	exists, err := tableColumnExists(ctx, s, "mail_identities", "autocrypt_enabled")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE mail_identities ADD COLUMN autocrypt_enabled INTEGER NOT NULL DEFAULT 1`)
	return err
}
