// File overview: Per-identity Sent and Drafts folder choices. These columns let
// each outgoing identity point at the correct IMAP folders when a user has more
// than one IMAP account or more than one role-assigned folder candidate.

package store

import "context"

func userIdentityMailboxMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion006,
		Label:   "user schema 006 identity mailboxes",
		After: []migrationStep{
			{Label: "add identity mailbox choices", Run: ensureIdentityMailboxColumns},
			{Label: "seed identity mailbox choices", Run: seedIdentityMailboxChoices},
		},
	}
}

func ensureIdentityMailboxColumns(ctx context.Context, s *Store) error {
	columns := []struct {
		Name string
		DDL  string
	}{
		{Name: "sent_mailbox_id", DDL: `ALTER TABLE mail_identities ADD COLUMN sent_mailbox_id INTEGER NOT NULL DEFAULT 0`},
		{Name: "drafts_mailbox_id", DDL: `ALTER TABLE mail_identities ADD COLUMN drafts_mailbox_id INTEGER NOT NULL DEFAULT 0`},
	}
	for _, column := range columns {
		exists, err := tableColumnExists(ctx, s, "mail_identities", column.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.DDL); err != nil {
			return err
		}
	}
	return nil
}

func seedIdentityMailboxChoices(ctx context.Context, s *Store) error {
	if err := ensureIdentityIMAPColumn(ctx, s); err != nil {
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
