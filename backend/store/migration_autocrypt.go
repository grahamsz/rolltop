// File overview: Shared helper for the per-identity Autocrypt key-discovery
// preference. The PGP plugin owns the user-facing migration, but older identity
// migrations still call this helper so startup remains idempotent.

package store

import "context"

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
