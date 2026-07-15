// File overview: Durable transfer and Inbox-arrival correlation used to distinguish deliveries from mailbox moves.

package store

func userInboxArrivalClassificationMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion022,
		Label:   "user schema 022 inbox arrival classification",
		Statements: []string{
			`ALTER TABLE messages ADD COLUMN canonical_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY`,
			`ALTER TABLE messages ADD COLUMN message_id_hash TEXT NOT NULL DEFAULT '' COLLATE BINARY`,
			`ALTER TABLE messages ADD COLUMN uid_validity INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS message_transfers (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				source_account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				destination_account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				source_mailbox_id INTEGER REFERENCES mailboxes(id) ON DELETE SET NULL,
				destination_mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				source_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
				source_uid INTEGER NOT NULL DEFAULT 0,
				source_uid_validity INTEGER NOT NULL DEFAULT 0,
				destination_uid INTEGER NOT NULL DEFAULT 0,
				destination_uid_validity INTEGER NOT NULL DEFAULT 0,
				operation_kind TEXT NOT NULL CHECK (operation_kind IN ('move', 'copy')),
				state TEXT NOT NULL CHECK (state IN ('pending', 'succeeded', 'failed', 'consumed')),
				raw_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				canonical_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				message_id_hash TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				internal_date_unix INTEGER NOT NULL DEFAULT 0,
				message_size INTEGER NOT NULL DEFAULT 0,
				consumed_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
				legacy_marker_id INTEGER UNIQUE,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				dispatched_at INTEGER NOT NULL DEFAULT 0,
				completed_at INTEGER NOT NULL DEFAULT 0,
				consumed_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_message_transfers_destination_uid
				ON message_transfers(user_id, destination_account_id, destination_mailbox_id, destination_uid, destination_uid_validity, expires_at, id)
				WHERE state = 'succeeded' AND destination_uid > 0`,
			`CREATE INDEX IF NOT EXISTS idx_message_transfers_raw
				ON message_transfers(user_id, destination_account_id, destination_mailbox_id, raw_sha256, expires_at, id)
				WHERE state = 'succeeded' AND raw_sha256 <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_message_transfers_canonical
				ON message_transfers(user_id, destination_account_id, destination_mailbox_id, canonical_sha256, expires_at, id)
				WHERE state = 'succeeded' AND canonical_sha256 <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_message_transfers_message_id
				ON message_transfers(user_id, destination_account_id, destination_mailbox_id, message_id_hash, internal_date_unix, message_size, expires_at, id)
				WHERE state = 'succeeded' AND message_id_hash <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_message_transfers_expiry
				ON message_transfers(user_id, expires_at, state)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_message_transfers_active_move_source
				ON message_transfers(user_id, source_account_id, source_mailbox_id, source_uid, source_uid_validity)
				WHERE source_mailbox_id IS NOT NULL AND operation_kind = 'move'
					AND state IN ('pending', 'succeeded')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_message_transfers_active_copy_operation
				ON message_transfers(user_id, source_account_id, source_mailbox_id, source_uid,
					source_uid_validity, destination_account_id, destination_mailbox_id)
				WHERE source_mailbox_id IS NOT NULL AND operation_kind = 'copy'
					AND state IN ('pending', 'succeeded')`,
			`CREATE TABLE IF NOT EXISTS expunged_message_fingerprints (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				source_mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				source_uid INTEGER NOT NULL DEFAULT 0,
				source_uid_validity INTEGER NOT NULL DEFAULT 0,
				raw_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				canonical_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				message_id_hash TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				internal_date_unix INTEGER NOT NULL DEFAULT 0,
				message_size INTEGER NOT NULL DEFAULT 0,
				consumed_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
				created_at INTEGER NOT NULL,
				consumed_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL,
				UNIQUE(user_id, account_id, source_mailbox_id, source_uid, source_uid_validity)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_expunged_message_fingerprints_raw
				ON expunged_message_fingerprints(user_id, account_id, raw_sha256, expires_at, id)
				WHERE consumed_at = 0 AND raw_sha256 <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_expunged_message_fingerprints_canonical
				ON expunged_message_fingerprints(user_id, account_id, canonical_sha256, expires_at, id)
				WHERE consumed_at = 0 AND canonical_sha256 <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_expunged_message_fingerprints_message_id
				ON expunged_message_fingerprints(user_id, account_id, message_id_hash, internal_date_unix, message_size, expires_at, id)
				WHERE consumed_at = 0 AND message_id_hash <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_expunged_message_fingerprints_expiry
				ON expunged_message_fingerprints(user_id, expires_at)`,
			`CREATE TABLE IF NOT EXISTS pending_inbox_arrivals (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				sync_run_id INTEGER REFERENCES sync_runs(id) ON DELETE SET NULL,
				classification TEXT NOT NULL CHECK (classification IN ('pending', 'delivery', 'local_move', 'local_copy', 'external_move')),
				raw_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				canonical_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				message_id_hash TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				internal_date_unix INTEGER NOT NULL DEFAULT 0,
				message_size INTEGER NOT NULL DEFAULT 0,
				matched_transfer_id INTEGER NOT NULL DEFAULT 0,
				matched_expunged_id INTEGER NOT NULL DEFAULT 0,
				event_id INTEGER REFERENCES new_mail_events(id) ON DELETE SET NULL,
				created_at INTEGER NOT NULL,
				available_at INTEGER NOT NULL,
				finalized_at INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, message_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_pending_inbox_arrivals_due
				ON pending_inbox_arrivals(user_id, account_id, classification, available_at, id)`,
			`CREATE INDEX IF NOT EXISTS idx_pending_inbox_arrivals_run
				ON pending_inbox_arrivals(user_id, sync_run_id, classification)`,
			`CREATE TABLE IF NOT EXISTS mailbox_generation_rebuilds (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				target_uid_validity INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, account_id, mailbox_id)
			)`,
			`CREATE TABLE IF NOT EXISTS mailbox_generation_rebuild_messages (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				target_uid_validity INTEGER NOT NULL,
				source_message_id INTEGER NOT NULL,
				source_uid INTEGER NOT NULL,
				raw_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				canonical_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				message_id_hash TEXT NOT NULL DEFAULT '' COLLATE BINARY,
				internal_date_unix INTEGER NOT NULL DEFAULT 0,
				message_size INTEGER NOT NULL DEFAULT 0,
				is_read INTEGER NOT NULL DEFAULT 0,
				read_sync_pending INTEGER NOT NULL DEFAULT 0,
				is_starred INTEGER NOT NULL DEFAULT 0,
				star_sync_pending INTEGER NOT NULL DEFAULT 0,
				has_snooze INTEGER NOT NULL DEFAULT 0,
				snooze_id INTEGER NOT NULL DEFAULT 0,
				snooze_thread_key TEXT NOT NULL DEFAULT '',
				snooze_generation INTEGER NOT NULL DEFAULT 0,
				snoozed_until INTEGER NOT NULL DEFAULT 0,
				snooze_reminded_at INTEGER NOT NULL DEFAULT 0,
				snooze_created_at INTEGER NOT NULL DEFAULT 0,
				snooze_updated_at INTEGER NOT NULL DEFAULT 0,
				has_new_mail_event INTEGER NOT NULL DEFAULT 0,
				new_mail_event_id INTEGER NOT NULL DEFAULT 0,
				new_mail_from_addr TEXT NOT NULL DEFAULT '',
				new_mail_subject TEXT NOT NULL DEFAULT '',
				new_mail_created_at INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, source_message_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_mailbox_generation_rebuild_target
				ON mailbox_generation_rebuild_messages(user_id, account_id, mailbox_id, target_uid_validity, id)`,
			`CREATE INDEX IF NOT EXISTS idx_mailbox_generation_rebuild_raw
				ON mailbox_generation_rebuild_messages(user_id, account_id, mailbox_id, target_uid_validity, raw_sha256, id)
				WHERE raw_sha256 <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_mailbox_generation_rebuild_canonical
				ON mailbox_generation_rebuild_messages(user_id, account_id, mailbox_id, target_uid_validity, canonical_sha256, id)
				WHERE canonical_sha256 <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_mailbox_generation_rebuild_message_id
				ON mailbox_generation_rebuild_messages(user_id, account_id, mailbox_id, target_uid_validity,
					message_id_hash, internal_date_unix, message_size, id)
				WHERE message_id_hash <> ''`,
			`CREATE TABLE IF NOT EXISTS mailbox_generation_rebuild_snooze_events (
				rebuild_message_id INTEGER NOT NULL REFERENCES mailbox_generation_rebuild_messages(id) ON DELETE CASCADE,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				original_event_id INTEGER NOT NULL,
				snooze_generation INTEGER NOT NULL,
				from_addr TEXT NOT NULL DEFAULT '',
				subject TEXT NOT NULL DEFAULT '',
				due_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, original_event_id)
			)`,
			`CREATE TABLE IF NOT EXISTS mailbox_generation_rebuild_unsubscribe_sends (
				rebuild_message_id INTEGER NOT NULL REFERENCES mailbox_generation_rebuild_messages(id) ON DELETE CASCADE,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				original_send_id INTEGER NOT NULL,
				sender TEXT NOT NULL DEFAULT '',
				unsubscribe_url TEXT NOT NULL,
				sent_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, original_send_id)
			)`,
			`CREATE TABLE IF NOT EXISTS mailbox_generation_blob_cleanup (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
				mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
				target_uid_validity INTEGER NOT NULL,
				blob_id INTEGER NOT NULL,
				blob_path TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, blob_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_mailbox_generation_blob_cleanup_scope
				ON mailbox_generation_blob_cleanup(user_id, account_id, mailbox_id, id)`,
			`INSERT OR IGNORE INTO message_transfers
				(user_id, source_account_id, destination_account_id, destination_mailbox_id, operation_kind, state, raw_sha256,
				 legacy_marker_id, created_at, updated_at, dispatched_at, completed_at, expires_at)
				SELECT user_id, account_id, account_id, destination_mailbox_id, 'move', 'succeeded', raw_sha256,
				 id, created_at, created_at, created_at, created_at, expires_at
				FROM pending_move_notifications
				WHERE consumed_message_id IS NULL AND expires_at > CAST(strftime('%s', 'now') AS INTEGER)`,
		},
	}
}
