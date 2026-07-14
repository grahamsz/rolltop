// File overview: Durable one-shot notification suppression for messages moved between IMAP mailboxes.

package store

func userPendingMoveNotificationMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion021,
		Label:   "user schema 021 pending move notifications",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS pending_move_notifications (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				destination_mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				raw_sha256 TEXT NOT NULL COLLATE BINARY,
				consumed_message_id INTEGER REFERENCES messages(id) ON DELETE CASCADE,
				created_at INTEGER NOT NULL,
				consumed_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_pending_move_notifications_lookup
				ON pending_move_notifications(user_id, account_id, destination_mailbox_id, raw_sha256, expires_at, id)
				WHERE consumed_message_id IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_pending_move_notifications_consumed
				ON pending_move_notifications(user_id, consumed_message_id, expires_at)
				WHERE consumed_message_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS idx_pending_move_notifications_expiry
				ON pending_move_notifications(user_id, expires_at)`,
		},
	}
}
