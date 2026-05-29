// File overview: System database migration 001. Store.OpenServerWithProgress
// applies this migration to data/rolltop.db before any user database is
// opened. The system database owns installation-level state: local users,
// browser sessions, plugin enablement, plugin migration bookkeeping, and
// administrator-managed remote-image blocklist rules. Mail headers, messages,
// contacts, sync runs, blobs, and per-user plugin caches intentionally live in
// the user database defined by migration_user_001.go.

package store

import (
	"context"

	"rolltop/backend/plugins"
)

// systemMigrationSet returns the single clean-start system schema. Auth and
// setup APIs read users/sessions from these tables, plugin settings drive the
// admin plugin UI, and remote-image handlers consult the blocklist rules while
// rendering message bodies.
func systemMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "system",
		Version: SystemSchemaVersion,
		Label:   "system schema",
		Statements: []string{
			// Users and sessions are the only authentication state kept at system scope.
			`CREATE TABLE IF NOT EXISTS users (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				email TEXT NOT NULL UNIQUE,
				name TEXT NOT NULL,
				password_hash TEXT NOT NULL,
				is_admin INTEGER NOT NULL DEFAULT 0,
				date_locale TEXT NOT NULL DEFAULT '',
				date_format TEXT NOT NULL DEFAULT 'mdy',
				theme TEXT NOT NULL DEFAULT 'classic',
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS sessions (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				token_hash TEXT NOT NULL UNIQUE,
				expires_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				last_seen_at INTEGER NOT NULL
			)`,
			// Plugin settings are global toggles; user-specific plugin data belongs in
			// migration_user_001.go next to the mail data it references.
			`CREATE TABLE IF NOT EXISTS plugin_settings (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				enabled INTEGER NOT NULL,
				enabled_by_default INTEGER NOT NULL,
				heavy INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS plugin_migrations (
				plugin_id TEXT NOT NULL,
				migration_id TEXT NOT NULL,
				applied_at INTEGER NOT NULL,
				app_version TEXT NOT NULL DEFAULT '',
				checksum TEXT NOT NULL,
				PRIMARY KEY(plugin_id, migration_id)
			)`,
			// Remote image block rules are admin-managed policy shared by all users.
			`CREATE TABLE IF NOT EXISTS plugin_remote_image_blocklist_rules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				pattern TEXT NOT NULL UNIQUE,
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash)`,
		},
		After: []migrationStep{
			{Label: "seed plugin settings", Run: func(ctx context.Context, s *Store) error { return s.seedPluginSettings(ctx) }},
			{Label: "seed remote image blocklist", Run: func(ctx context.Context, s *Store) error {
				for _, hook := range plugins.Hooks(plugins.RemoteImageBlocklist) {
					typed, ok := hook.(plugins.RemoteImageBlocklistHook)
					if ok {
						return typed.SeedRemoteImageRules(ctx, s.db)
					}
				}
				return nil
			}},
		},
	}
}
