// File overview: Durable tenant-scoped cleanup queue for unreferenced blob files.

package store

func userBlobCleanupQueueMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion025,
		Label:   "generic blob cleanup queue",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS blob_cleanup_queue (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				blob_id INTEGER NOT NULL,
				blob_path TEXT NOT NULL,
				blob_sha256 TEXT NOT NULL DEFAULT '',
				blob_size INTEGER NOT NULL DEFAULT 0,
				blob_created_at INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, blob_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_blob_cleanup_queue_user
				ON blob_cleanup_queue(user_id, id)`,
		},
	}
}
