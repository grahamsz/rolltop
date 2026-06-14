// File overview: Additional indexes for cold all-mail and mailbox conversation lists.

package store

func userMessageListIndexMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion012,
		Label:   "user schema 012 message list indexes",
		Statements: []string{
			`CREATE INDEX IF NOT EXISTS idx_messages_user_thread_latest ON messages(user_id, thread_key, date_unix DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_mailbox_thread_latest ON messages(user_id, mailbox_id, thread_key, date_unix DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_mailboxes_user_all_mail ON mailboxes(user_id, show_in_all_mail, id)`,
		},
	}
}
