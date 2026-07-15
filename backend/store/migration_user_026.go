// File overview: Durable new-arrival boundary for crash-resumable mailbox generation rebuilds.

package store

func userMailboxGenerationArrivalFloorMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion026,
		Label:   "user schema 026 mailbox generation arrival floor",
		Statements: []string{
			`ALTER TABLE mailbox_generation_rebuilds
				ADD COLUMN arrival_uid_floor INTEGER NOT NULL DEFAULT 0`,
		},
	}
}
