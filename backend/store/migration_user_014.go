// File overview: User-scoped Web Push subscription endpoints.

package store

func userWebPushSubscriptionMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion014,
		Label:   "user schema 014 web push subscriptions",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS web_push_subscriptions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				endpoint TEXT NOT NULL,
				p256dh TEXT NOT NULL,
				auth TEXT NOT NULL,
				user_agent TEXT NOT NULL DEFAULT '',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				last_seen_at INTEGER NOT NULL,
				UNIQUE(user_id, endpoint)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_web_push_subscriptions_user_updated ON web_push_subscriptions(user_id, updated_at DESC)`,
		},
	}
}
