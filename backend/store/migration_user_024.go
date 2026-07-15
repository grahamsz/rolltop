// File overview: Attempt-scoped ownership for crash-safe remote transfer dispatch recovery.

package store

func userTransferDispatchRecoveryMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion024,
		Label:   "user schema 024 transfer dispatch recovery",
		Statements: []string{
			`ALTER TABLE message_transfers ADD COLUMN dispatch_owner TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE message_transfers ADD COLUMN dispatch_attempt INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE message_transfers ADD COLUMN dispatch_finished_at INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE message_transfers ADD COLUMN destination_snapshot_uid_validity INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE message_transfers ADD COLUMN destination_snapshot_uid_next INTEGER NOT NULL DEFAULT 0`,
		},
	}
}
