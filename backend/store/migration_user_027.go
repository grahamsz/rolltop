// File overview: Durable completion boundary for multi-step message imports.

package store

func userMessageImportCompletionMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion027,
		Label:   "user schema 027 message import completion",
		Statements: []string{
			`ALTER TABLE messages
				ADD COLUMN import_completed_at INTEGER NOT NULL DEFAULT 0`,
			`UPDATE messages
				SET import_completed_at = CASE
					WHEN updated_at > 0 THEN updated_at
					WHEN created_at > 0 THEN created_at
					ELSE 1
				END
				WHERE import_completed_at = 0
					AND attachment_indexed_at > 0
					AND EXISTS (
						SELECT 1 FROM mailboxes
						WHERE mailboxes.user_id = messages.user_id
							AND mailboxes.account_id = messages.account_id
							AND mailboxes.id = messages.mailbox_id
							AND messages.uid <= mailboxes.last_uid
							AND (messages.uid_validity = 0 OR mailboxes.uidvalidity = 0
								OR messages.uid_validity = mailboxes.uidvalidity)
					)`,
		},
	}
}
