// File overview: Per-identity IMAP server choices. These columns let each
// outgoing identity pick which local IMAP account owns its Sent and Drafts
// folder selections when a user mirrors multiple mailboxes.

package store

import "context"

func userIdentityIMAPMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion007,
		Label:   "user schema 007 identity imap accounts",
		After: []migrationStep{
			{Label: "add identity imap account choice", Run: ensureIdentityIMAPColumn},
			{Label: "seed identity imap account choices", Run: seedIdentityIMAPChoices},
		},
	}
}

func ensureIdentityIMAPColumn(ctx context.Context, s *Store) error {
	exists, err := tableColumnExists(ctx, s, "mail_identities", "imap_account_id")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE mail_identities ADD COLUMN imap_account_id INTEGER NOT NULL DEFAULT 0`)
	return err
}

func seedIdentityIMAPChoices(ctx context.Context, s *Store) error {
	if err := ensureIdentityAutocryptColumn(ctx, s); err != nil {
		return err
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		if err := s.EnsureMailIdentityMailboxDefaults(ctx, user.ID); err != nil {
			return err
		}
	}
	return nil
}
