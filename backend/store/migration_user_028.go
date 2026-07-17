// File overview: Durable purge state and a covering index for inexpensive per-mailbox full-text progress.

package store

func userSearchProgressIndexMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion028,
		Label:   "user schema 028 search progress state",
		Statements: []string{
			`ALTER TABLE mailboxes ADD COLUMN search_index_purged INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE mailboxes ADD COLUMN search_index_state_known INTEGER NOT NULL DEFAULT 1`,
			// Existing indexes may have been deliberately purged or damaged under an
			// older release, and SQLite alone cannot distinguish that from a healthy
			// Bleve index. Leave them unverified until the next exact repair instead
			// of guessing from mutable sync-run display fields.
			`UPDATE mailboxes SET search_index_state_known = 0`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_mailbox_search_committed
				ON messages(user_id, mailbox_id, id)
				WHERE attachment_indexed_at > 0`,
		},
	}
}
