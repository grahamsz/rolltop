// File overview: Additional per-user schema indexes added after the initial V1 schema.

package store

func userSenderStatsMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion002,
		Label:   "user schema 002 sender stats index",
		Statements: []string{
			`CREATE INDEX IF NOT EXISTS idx_messages_user_from_read ON messages(user_id, from_addr, is_read)`,
		},
	}
}
