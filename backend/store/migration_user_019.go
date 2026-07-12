// File overview: Adds tenant-scoped Android message swipe preferences and archive-folder mappings.

package store

func userSwipePreferencesMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion019,
		Label:   "user schema 019 swipe preferences",
		Statements: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_mail_accounts_user_account
				ON mail_accounts(user_id, id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_mailboxes_user_account_mailbox
				ON mailboxes(user_id, account_id, id)`,
			`CREATE TABLE IF NOT EXISTS swipe_preferences (
				user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				left_action TEXT NOT NULL DEFAULT 'snooze'
					CHECK(left_action IN ('trash', 'archive', 'snooze', 'mark_read', 'mark_unread')),
				left_snooze_preset TEXT NOT NULL DEFAULT 'tomorrow'
					CHECK(left_snooze_preset IN ('later_today', 'tomorrow', 'next_week')),
				right_action TEXT NOT NULL DEFAULT 'mark_read'
					CHECK(right_action IN ('trash', 'archive', 'snooze', 'mark_read', 'mark_unread')),
				right_snooze_preset TEXT NOT NULL DEFAULT 'tomorrow'
					CHECK(right_snooze_preset IN ('later_today', 'tomorrow', 'next_week')),
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS swipe_archive_mailboxes (
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL,
				mailbox_id INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, account_id),
				UNIQUE(user_id, mailbox_id),
				FOREIGN KEY(user_id, account_id) REFERENCES mail_accounts(user_id, id) ON DELETE CASCADE,
				FOREIGN KEY(user_id, account_id, mailbox_id) REFERENCES mailboxes(user_id, account_id, id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_swipe_archive_mailboxes_user_mailbox
				ON swipe_archive_mailboxes(user_id, mailbox_id)`,
		},
	}
}
