// File overview: Language detection plugin support for message language indexing and filters. migration declarations. Plugin schema migration declarations and helper persistence.

package language_search

import (
	"context"
	"database/sql"

	"mailmirror/backend/plugins"
)

func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		PluginID: plugins.LanguageSearch,
		ID:       "001_create_language_messages",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS plugin_language_messages (
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				language_code TEXT NOT NULL,
				detected_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, message_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_language_messages_user_language ON plugin_language_messages(user_id, language_code)`,
		},
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO plugin_language_messages
					(user_id, message_id, language_code, detected_at)
				SELECT user_id, id, lower(trim(language_code)), updated_at
				FROM messages
				WHERE trim(language_code) <> ''`)
			return err
		},
	}}
}
