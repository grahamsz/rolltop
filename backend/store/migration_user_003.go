// File overview: Materialized sender-read ranking stats for fast best-match search.

package store

import "context"

func userSenderStatsTableMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion003,
		Label:   "user schema 003 sender stats table",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS sender_read_stats (
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				sender TEXT NOT NULL,
				read_count INTEGER NOT NULL DEFAULT 0,
				total_count INTEGER NOT NULL DEFAULT 0,
				boost REAL NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, sender)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sender_read_stats_user_boost ON sender_read_stats(user_id, boost DESC, read_count DESC, sender)`,
		},
		After: []migrationStep{
			{Label: "refresh sender stats", Run: refreshAllUserSenderStats},
		},
	}
}

func refreshAllUserSenderStats(ctx context.Context, s *Store) error {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		if err := s.RefreshReadSenderStatsForUser(ctx, user.ID); err != nil {
			return err
		}
	}
	return nil
}
