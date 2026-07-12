// File overview: Adds durable per-subscription cursors for new-mail push delivery.

package store

func userWebPushDeliveryCursorMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion017,
		Label:   "user schema 017 web push delivery cursors",
		Statements: []string{
			`ALTER TABLE web_push_subscriptions ADD COLUMN last_new_mail_event_id INTEGER NOT NULL DEFAULT 0`,
			`UPDATE web_push_subscriptions
				SET last_new_mail_event_id = COALESCE((
					SELECT MAX(events.id)
					FROM new_mail_events AS events
					WHERE events.user_id = web_push_subscriptions.user_id
				), 0)`,
		},
	}
}
