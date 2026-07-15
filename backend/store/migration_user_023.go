// File overview: Durable pending Inbox-arrival state during mailbox generation rebuilds.

package store

func userMailboxGenerationArrivalJournalMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion023,
		Label:   "mailbox generation arrival journal",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS mailbox_generation_rebuild_inbox_arrivals (
				rebuild_message_id INTEGER NOT NULL REFERENCES mailbox_generation_rebuild_messages(id) ON DELETE CASCADE,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				original_arrival_id INTEGER NOT NULL,
				sync_run_id INTEGER REFERENCES sync_runs(id) ON DELETE SET NULL,
				classification TEXT NOT NULL CHECK (classification IN ('pending', 'delivery', 'local_move', 'local_copy', 'external_move')),
				raw_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				canonical_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				message_id_hash TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				internal_date_unix INTEGER NOT NULL DEFAULT 0,
				message_size INTEGER NOT NULL DEFAULT 0,
				matched_transfer_id INTEGER NOT NULL DEFAULT 0,
				matched_expunged_id INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				available_at INTEGER NOT NULL,
				finalized_at INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, original_arrival_id),
				UNIQUE(rebuild_message_id)
			)`,
		},
	}
}
