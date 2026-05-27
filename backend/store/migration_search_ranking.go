// File overview: User search-ranking preference columns for sender-history and
// contact-book boosts. The columns live in both the system users table and the
// mirrored tenant users table so request authentication and user-scoped joins
// see the same query-time search settings.

package store

import "context"

func systemUserSearchRankingMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "system",
		Version: SystemSchemaVersion003,
		Label:   "system schema 003 search ranking weights",
		After: []migrationStep{
			{Label: "add user search ranking weights", Run: ensureUserSearchRankingColumns},
		},
	}
}

func userSearchRankingMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion005,
		Label:   "user schema 005 search ranking weights",
		After: []migrationStep{
			{Label: "add mirrored user search ranking weights", Run: ensureUserSearchRankingColumns},
		},
	}
}

func ensureUserSearchRankingColumns(ctx context.Context, s *Store) error {
	columns := []struct {
		Name string
		DDL  string
	}{
		{Name: "search_sender_history", DDL: `ALTER TABLE users ADD COLUMN search_sender_history TEXT NOT NULL DEFAULT 'normal'`},
		{Name: "search_contact_boost", DDL: `ALTER TABLE users ADD COLUMN search_contact_boost TEXT NOT NULL DEFAULT 'normal'`},
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
