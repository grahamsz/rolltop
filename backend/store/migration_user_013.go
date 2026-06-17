// File overview: User-scoped remote image cache metadata.

package store

func userRemoteImageCacheMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion013,
		Label:   "user schema 013 remote image cache",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS remote_image_cache (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				url_hash TEXT NOT NULL,
				url TEXT NOT NULL,
				blob_id INTEGER NOT NULL DEFAULT 0,
				blob_path TEXT NOT NULL DEFAULT '',
				content_type TEXT NOT NULL DEFAULT '',
				size INTEGER NOT NULL DEFAULT 0,
				status TEXT NOT NULL,
				error TEXT NOT NULL DEFAULT '',
				fetched_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, url_hash)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_remote_image_cache_user_status ON remote_image_cache(user_id, status, expires_at)`,
		},
	}
}
