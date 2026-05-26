// File overview: User search-behavior preference columns for system and tenant user mirrors.

package store

import "context"

func systemUserSearchPreferencesMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "system",
		Version: SystemSchemaVersion002,
		Label:   "system schema 002 user search preferences",
		After: []migrationStep{
			{Label: "add user search preferences", Run: ensureUserSearchPreferenceColumns},
		},
	}
}

func userSearchPreferencesMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion004,
		Label:   "user schema 004 search preferences",
		After: []migrationStep{
			{Label: "add mirrored user search preferences", Run: ensureUserSearchPreferenceColumns},
		},
	}
}

func ensureUserSearchPreferenceColumns(ctx context.Context, s *Store) error {
	columns := []struct {
		Name string
		DDL  string
	}{
		{Name: "search_preset", DDL: `ALTER TABLE users ADD COLUMN search_preset TEXT NOT NULL DEFAULT 'balanced'`},
		{Name: "search_recency_bias", DDL: `ALTER TABLE users ADD COLUMN search_recency_bias TEXT NOT NULL DEFAULT 'normal'`},
		{Name: "search_fuzzy", DDL: `ALTER TABLE users ADD COLUMN search_fuzzy TEXT NOT NULL DEFAULT 'balanced'`},
		{Name: "search_sender_boost", DDL: `ALTER TABLE users ADD COLUMN search_sender_boost INTEGER NOT NULL DEFAULT 1`},
		{Name: "search_attachment_weight", DDL: `ALTER TABLE users ADD COLUMN search_attachment_weight TEXT NOT NULL DEFAULT 'normal'`},
		{Name: "search_compact_splitting", DDL: `ALTER TABLE users ADD COLUMN search_compact_splitting INTEGER NOT NULL DEFAULT 1`},
	}
	for _, column := range columns {
		exists, err := tableColumnExists(ctx, s, "users", column.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.DDL); err != nil {
			return err
		}
	}
	return nil
}

func tableColumnExists(ctx context.Context, s *Store, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
