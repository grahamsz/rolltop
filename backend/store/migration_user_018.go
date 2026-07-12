// File overview: Adds local conversation snoozes and an independent durable reminder feed.

package store

func userSnoozeMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion018,
		Label:   "user schema 018 local snooze reminders",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS message_snoozes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				thread_key TEXT NOT NULL,
				generation INTEGER NOT NULL DEFAULT 1,
				snoozed_until INTEGER NOT NULL,
				reminded_at INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, thread_key),
				UNIQUE(user_id, message_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_message_snoozes_user_due
				ON message_snoozes(user_id, reminded_at, snoozed_until)`,
			`CREATE TABLE IF NOT EXISTS snooze_reminder_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				snooze_generation INTEGER NOT NULL,
				from_addr TEXT NOT NULL DEFAULT '',
				subject TEXT NOT NULL DEFAULT '',
				due_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				UNIQUE(user_id, message_id, snooze_generation)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_snooze_reminder_events_user_id
				ON snooze_reminder_events(user_id, id DESC)`,
			`ALTER TABLE web_push_subscriptions ADD COLUMN last_snooze_reminder_event_id INTEGER NOT NULL DEFAULT 0`,
			`UPDATE web_push_subscriptions
				SET last_snooze_reminder_event_id = COALESCE((
					SELECT MAX(events.id)
					FROM snooze_reminder_events AS events
					WHERE events.user_id = web_push_subscriptions.user_id
				), 0)`,
		},
	}
}
