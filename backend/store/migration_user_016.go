// File overview: Adds the durable, user-scoped arrival feed consumed by native notifications.

package store

func userNewMailEventMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion016,
		Label:   "user schema 016 new mail notification events",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS new_mail_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				from_addr TEXT NOT NULL DEFAULT '',
				subject TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				UNIQUE(user_id, message_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_new_mail_events_user_id ON new_mail_events(user_id, id DESC)`,
		},
	}
}
