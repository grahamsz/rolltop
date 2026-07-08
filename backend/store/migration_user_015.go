// File overview: Adds latest-message deep-link metadata to sync runs.

package store

import "context"

func userSyncRunLatestMessageMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion015,
		Label:   "user schema 015 sync run latest message links",
		After: []migrationStep{
			{Label: "add latest sync-run message id", Run: ensureSyncRunLatestMessageColumn},
		},
	}
}

func ensureSyncRunLatestMessageColumn(ctx context.Context, s *Store) error {
	exists, err := tableColumnExists(ctx, s, "sync_runs", "latest_new_message_id")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE sync_runs ADD COLUMN latest_new_message_id INTEGER NOT NULL DEFAULT 0`)
	return err
}
