// File overview: Backfill the exact, provider-standard Junk mailbox names used
// before Rolltop preserved RFC 6154 special-use attributes during discovery.

package store

import "context"

func userJunkMailboxRoleMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion020,
		Label:   "junk mailbox role",
		After: []migrationStep{{
			Label: "backfill exact junk mailbox roles",
			Run:   seedJunkMailboxRoles,
		}},
	}
}

func seedJunkMailboxRoles(ctx context.Context, s *Store) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailboxes
		SET role = 'junk',
			icon = CASE WHEN icon = 'folder' THEN 'report' ELSE icon END,
			show_in_all_mail = 0,
			updated_at = ?
		WHERE role = ''
		AND lower(trim(name)) IN ('junk', 'spam', 'junk e-mail', 'junk email', '[gmail]/spam')
		AND NOT EXISTS (
			SELECT 1 FROM mailboxes assigned
			WHERE assigned.user_id = mailboxes.user_id
			AND assigned.account_id = mailboxes.account_id
			AND assigned.role = 'junk'
			AND assigned.id <> mailboxes.id
		)`, nowUnix())
	return err
}
