package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var ErrNotFound = sql.ErrNoRows

const DefaultMessageBodyPreviewBytes = 4096

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`PRAGMA foreign_keys = ON`,
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
		`CREATE TABLE IF NOT EXISTS mail_accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
			email TEXT NOT NULL,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			username TEXT NOT NULL,
			encrypted_password TEXT NOT NULL,
			use_tls INTEGER NOT NULL DEFAULT 1,
			smtp_host TEXT NOT NULL DEFAULT '',
			smtp_port INTEGER NOT NULL DEFAULT 587,
			smtp_username TEXT NOT NULL DEFAULT '',
			encrypted_smtp_password TEXT NOT NULL DEFAULT '',
			smtp_use_tls INTEGER NOT NULL DEFAULT 1,
			mailbox TEXT NOT NULL DEFAULT 'INBOX',
			sync_interval_minutes INTEGER NOT NULL DEFAULT 15,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mailboxes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			sync_mode TEXT NOT NULL DEFAULT 'auto',
			role TEXT NOT NULL DEFAULT '',
			icon TEXT NOT NULL DEFAULT 'folder',
			show_in_sidebar INTEGER NOT NULL DEFAULT 1,
			show_in_all_mail INTEGER NOT NULL DEFAULT 1,
			include_in_search INTEGER NOT NULL DEFAULT 1,
			uidvalidity INTEGER NOT NULL DEFAULT 0,
			last_uid INTEGER NOT NULL DEFAULT 0,
			remote_message_count INTEGER NOT NULL DEFAULT 0,
			remote_unread_count INTEGER NOT NULL DEFAULT 0,
			remote_uid_next INTEGER NOT NULL DEFAULT 0,
			status_checked_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(user_id, account_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS blobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			path TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			size INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(user_id, path)
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
			mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
			blob_id INTEGER NOT NULL REFERENCES blobs(id) ON DELETE RESTRICT,
			message_id_header TEXT NOT NULL DEFAULT '',
			in_reply_to TEXT NOT NULL DEFAULT '',
			references_header TEXT NOT NULL DEFAULT '',
			thread_key TEXT NOT NULL DEFAULT '',
			thread_headers_checked_at INTEGER NOT NULL DEFAULT 0,
			subject TEXT NOT NULL DEFAULT '',
			language_code TEXT NOT NULL DEFAULT '',
			from_addr TEXT NOT NULL DEFAULT '',
			to_addr TEXT NOT NULL DEFAULT '',
			cc_addr TEXT NOT NULL DEFAULT '',
			date_unix INTEGER NOT NULL DEFAULT 0,
			internal_date_unix INTEGER NOT NULL DEFAULT 0,
			uid INTEGER NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			blob_path TEXT NOT NULL,
			body_text TEXT NOT NULL DEFAULT '',
			body_html TEXT NOT NULL DEFAULT '',
			is_read INTEGER NOT NULL DEFAULT 0,
			read_sync_pending INTEGER NOT NULL DEFAULT 0,
			is_starred INTEGER NOT NULL DEFAULT 0,
			star_sync_pending INTEGER NOT NULL DEFAULT 0,
			has_attachments INTEGER NOT NULL DEFAULT 0,
			attachment_indexed_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			UNIQUE(user_id, account_id, mailbox_id, uid)
		)`,
		`CREATE TABLE IF NOT EXISTS locations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
			uid INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(user_id, message_id, mailbox_id, uid)
		)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			blob_id INTEGER NOT NULL REFERENCES blobs(id) ON DELETE RESTRICT,
			filename TEXT NOT NULL,
			content_type TEXT NOT NULL,
			content_id TEXT NOT NULL DEFAULT '',
			is_inline INTEGER NOT NULL DEFAULT 0,
			size INTEGER NOT NULL,
			blob_path TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sync_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
			status TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			messages_seen INTEGER NOT NULL DEFAULT 0,
			messages_stored INTEGER NOT NULL DEFAULT 0,
			messages_skipped INTEGER NOT NULL DEFAULT 0,
			new_messages INTEGER NOT NULL DEFAULT 0,
			messages_total INTEGER NOT NULL DEFAULT 0,
			mailboxes_done INTEGER NOT NULL DEFAULT 0,
			mailboxes_total INTEGER NOT NULL DEFAULT 0,
			current_mailbox TEXT NOT NULL DEFAULT '',
			current_uid INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_user_date ON messages(user_id, date_unix DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_user_mailbox_read ON messages(user_id, mailbox_id, is_read)`,
		`CREATE INDEX IF NOT EXISTS idx_attachments_user_message ON attachments(user_id, message_id)`,
		`CREATE INDEX IF NOT EXISTS idx_blobs_user ON blobs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_runs_user ON sync_runs(user_id, started_at DESC)`,
		`CREATE TABLE IF NOT EXISTS trusted_image_senders (
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			sender TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY(user_id, sender)
		)`,
		`CREATE TABLE IF NOT EXISTS remote_image_block_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pattern TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS one_click_unsubscribe_sends (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			sender TEXT NOT NULL DEFAULT '',
			unsubscribe_url TEXT NOT NULL,
			sent_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(user_id, message_id, unsubscribe_url)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_one_click_unsubscribe_user_message ON one_click_unsubscribe_sends(user_id, message_id, sent_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_one_click_unsubscribe_user_url ON one_click_unsubscribe_sends(user_id, unsubscribe_url, sent_at DESC)`,
		`CREATE TABLE IF NOT EXISTS bimi_icons (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			domain TEXT NOT NULL,
			logo_url TEXT NOT NULL DEFAULT '',
			svg TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			fetched_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(user_id, domain)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bimi_icons_user_domain ON bimi_icons(user_id, domain)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"date_locale", `date_locale TEXT NOT NULL DEFAULT ''`},
		{"date_format", `date_format TEXT NOT NULL DEFAULT 'mdy'`},
		{"theme", `theme TEXT NOT NULL DEFAULT 'classic'`},
	} {
		if err := s.ensureColumn(ctx, "users", col.name, col.def); err != nil {
			return err
		}
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"smtp_host", `smtp_host TEXT NOT NULL DEFAULT ''`},
		{"smtp_port", `smtp_port INTEGER NOT NULL DEFAULT 587`},
		{"smtp_username", `smtp_username TEXT NOT NULL DEFAULT ''`},
		{"encrypted_smtp_password", `encrypted_smtp_password TEXT NOT NULL DEFAULT ''`},
		{"smtp_use_tls", `smtp_use_tls INTEGER NOT NULL DEFAULT 1`},
	} {
		if err := s.ensureColumn(ctx, "mail_accounts", col.name, col.def); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "mailboxes", "sync_mode", `sync_mode TEXT NOT NULL DEFAULT 'auto'`); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"role", `role TEXT NOT NULL DEFAULT ''`},
		{"icon", `icon TEXT NOT NULL DEFAULT 'folder'`},
		{"show_in_sidebar", `show_in_sidebar INTEGER NOT NULL DEFAULT 1`},
		{"show_in_all_mail", `show_in_all_mail INTEGER NOT NULL DEFAULT 1`},
		{"include_in_search", `include_in_search INTEGER NOT NULL DEFAULT 1`},
	} {
		if err := s.ensureColumn(ctx, "mailboxes", col.name, col.def); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET role = 'inbox', icon = 'inbox', updated_at = ?
		WHERE role = '' AND lower(name) = 'inbox'`, nowUnix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET role = 'trash', icon = 'delete', show_in_all_mail = 0, updated_at = ?
		WHERE role = '' AND lower(name) IN ('trash', 'deleted', 'deleted items', '[gmail]/trash')`, nowUnix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mailboxes AS child SET sync_mode = 'inherit', updated_at = ?
		WHERE child.sync_mode = 'auto' AND EXISTS (
			SELECT 1 FROM mailboxes AS parent
			WHERE parent.user_id = child.user_id AND parent.account_id = child.account_id AND parent.id <> child.id
				AND (substr(child.name, 1, length(parent.name) + 1) = parent.name || '.'
					OR substr(child.name, 1, length(parent.name) + 1) = parent.name || '/'
					OR substr(child.name, 1, length(parent.name) + 1) = parent.name || '\')
		)`, nowUnix()); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"remote_message_count", `remote_message_count INTEGER NOT NULL DEFAULT 0`},
		{"remote_unread_count", `remote_unread_count INTEGER NOT NULL DEFAULT 0`},
		{"remote_uid_next", `remote_uid_next INTEGER NOT NULL DEFAULT 0`},
		{"status_checked_at", `status_checked_at INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := s.ensureColumn(ctx, "mailboxes", col.name, col.def); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "messages", "body_html", `body_html TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "in_reply_to", `in_reply_to TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "references_header", `references_header TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "thread_key", `thread_key TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "thread_headers_checked_at", `thread_headers_checked_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "language_code", `language_code TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "is_read", `is_read INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "read_sync_pending", `read_sync_pending INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "is_starred", `is_starred INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "star_sync_pending", `star_sync_pending INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "has_attachments", `has_attachments INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "messages", "attachment_indexed_at", `attachment_indexed_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "attachments", "is_inline", `is_inline INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_user_thread ON messages(user_id, thread_key, date_unix, id)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_user_mailbox_thread ON messages(user_id, mailbox_id, thread_key, date_unix, id)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_user_starred ON messages(user_id, is_starred, date_unix DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_messages_thread_headers_checked ON messages(thread_headers_checked_at, id)`); err != nil {
		return err
	}
	if err := s.seedRemoteImageBlockRules(ctx); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"updated_at", `updated_at INTEGER NOT NULL DEFAULT 0`},
		{"messages_skipped", `messages_skipped INTEGER NOT NULL DEFAULT 0`},
		{"new_messages", `new_messages INTEGER NOT NULL DEFAULT 0`},
		{"messages_total", `messages_total INTEGER NOT NULL DEFAULT 0`},
		{"mailboxes_done", `mailboxes_done INTEGER NOT NULL DEFAULT 0`},
		{"mailboxes_total", `mailboxes_total INTEGER NOT NULL DEFAULT 0`},
		{"current_mailbox", `current_mailbox TEXT NOT NULL DEFAULT ''`},
		{"current_uid", `current_uid INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := s.ensureColumn(ctx, "sync_runs", col.name, col.def); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+definition)
	return err
}

func nowUnix() int64 {
	return time.Now().UTC().Unix()
}

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
