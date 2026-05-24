package store

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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

func cleanEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash string, isAdmin bool) (User, error) {
	email = cleanEmail(email)
	name = strings.TrimSpace(name)
	if email == "" || name == "" || passwordHash == "" {
		return User{}, errors.New("email, name, and password hash are required")
	}
	ts := nowUnix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (email, name, password_hash, is_admin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, email, name, passwordHash, boolInt(isAdmin), ts, ts)
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, id)
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	var u User
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, is_admin, date_locale, date_format, created_at, updated_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &created, &updated)
	u.CreatedAt = unixTime(created)
	u.UpdatedAt = unixTime(updated)
	u.DateFormat = normalizeUserDateFormat(u.DateFormat)
	return u, err
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, is_admin, date_locale, date_format, created_at, updated_at FROM users WHERE email = ?`, cleanEmail(email)).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &created, &updated)
	u.CreatedAt = unixTime(created)
	u.UpdatedAt = unixTime(updated)
	u.DateFormat = normalizeUserDateFormat(u.DateFormat)
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, name, password_hash, is_admin, date_locale, date_format, created_at, updated_at FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var created, updated int64
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &created, &updated); err != nil {
			return nil, err
		}
		u.CreatedAt = unixTime(created)
		u.UpdatedAt = unixTime(updated)
		u.DateFormat = normalizeUserDateFormat(u.DateFormat)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUserDisplayPreferences(ctx context.Context, userID int64, dateLocale, dateFormat string) (User, error) {
	dateLocale = strings.TrimSpace(dateLocale)
	if len(dateLocale) > 64 {
		dateLocale = dateLocale[:64]
	}
	dateFormat = normalizeUserDateFormat(dateFormat)
	_, err := s.db.ExecContext(ctx, `UPDATE users SET date_locale = ?, date_format = ?, updated_at = ? WHERE id = ?`,
		dateLocale, dateFormat, nowUnix(), userID)
	if err != nil {
		return User{}, err
	}
	return s.GetUserByID(ctx, userID)
}

func normalizeUserDateFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "locale", "dmy", "ymd":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "mdy"
	}
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) (Session, error) {
	ts := nowUnix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions (user_id, token_hash, expires_at, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)`, userID, tokenHash, expiresAt.UTC().Unix(), ts, ts)
	if err != nil {
		return Session{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Session{}, err
	}
	return Session{ID: id, UserID: userID, TokenHash: tokenHash, ExpiresAt: expiresAt.UTC(), CreatedAt: unixTime(ts), LastSeenAt: unixTime(ts)}, nil
}

func (s *Store) GetSessionUser(ctx context.Context, tokenHash string) (Session, User, error) {
	var sess Session
	var u User
	var expires, created, lastSeen, userCreated, userUpdated int64
	err := s.db.QueryRowContext(ctx, `SELECT
			s.id, s.user_id, s.token_hash, s.expires_at, s.created_at, s.last_seen_at,
				u.id, u.email, u.name, u.password_hash, u.is_admin, u.date_locale, u.date_format, u.created_at, u.updated_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, nowUnix()).
		Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &expires, &created, &lastSeen,
			&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.IsAdmin, &u.DateLocale, &u.DateFormat, &userCreated, &userUpdated)
	if err != nil {
		return Session{}, User{}, err
	}
	sess.ExpiresAt = unixTime(expires)
	sess.CreatedAt = unixTime(created)
	sess.LastSeenAt = unixTime(lastSeen)
	u.CreatedAt = unixTime(userCreated)
	u.UpdatedAt = unixTime(userUpdated)
	u.DateFormat = normalizeUserDateFormat(u.DateFormat)
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, nowUnix(), sess.ID)
	return sess, u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, nowUnix())
	return err
}

func (s *Store) UpsertMailAccount(ctx context.Context, a MailAccount) (MailAccount, error) {
	if a.UserID == 0 || strings.TrimSpace(a.Email) == "" || strings.TrimSpace(a.Host) == "" || strings.TrimSpace(a.Username) == "" || a.Port == 0 || a.EncryptedPassword == "" {
		return MailAccount{}, errors.New("mail account fields are incomplete")
	}
	if strings.TrimSpace(a.Mailbox) == "" {
		a.Mailbox = "INBOX"
	}
	if strings.TrimSpace(a.SMTPHost) == "" {
		a.SMTPHost = a.Host
	}
	if a.SMTPPort == 0 {
		a.SMTPPort = 587
	}
	if strings.TrimSpace(a.SMTPUsername) == "" {
		a.SMTPUsername = a.Username
	}
	if a.EncryptedSMTPPassword == "" {
		a.EncryptedSMTPPassword = a.EncryptedPassword
	}
	if a.SyncIntervalMinutes <= 0 {
		a.SyncIntervalMinutes = 15
	}
	ts := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO mail_accounts
			(user_id, email, host, port, username, encrypted_password, use_tls, smtp_host, smtp_port, smtp_username, encrypted_smtp_password, smtp_use_tls, mailbox, sync_interval_minutes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			email = excluded.email,
			host = excluded.host,
			port = excluded.port,
			username = excluded.username,
			encrypted_password = excluded.encrypted_password,
			use_tls = excluded.use_tls,
			smtp_host = excluded.smtp_host,
			smtp_port = excluded.smtp_port,
			smtp_username = excluded.smtp_username,
			encrypted_smtp_password = excluded.encrypted_smtp_password,
			smtp_use_tls = excluded.smtp_use_tls,
			mailbox = excluded.mailbox,
			sync_interval_minutes = excluded.sync_interval_minutes,
			updated_at = excluded.updated_at`,
		a.UserID, strings.TrimSpace(a.Email), strings.TrimSpace(a.Host), a.Port, strings.TrimSpace(a.Username), a.EncryptedPassword,
		boolInt(a.UseTLS), strings.TrimSpace(a.SMTPHost), a.SMTPPort, strings.TrimSpace(a.SMTPUsername), a.EncryptedSMTPPassword,
		boolInt(a.SMTPUseTLS), strings.TrimSpace(a.Mailbox), a.SyncIntervalMinutes, ts, ts)
	if err != nil {
		return MailAccount{}, err
	}
	return s.GetMailAccount(ctx, a.UserID)
}

func (s *Store) GetMailAccount(ctx context.Context, userID int64) (MailAccount, error) {
	var a MailAccount
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, email, host, port, username, encrypted_password, use_tls, smtp_host, smtp_port, smtp_username, encrypted_smtp_password, smtp_use_tls, mailbox, sync_interval_minutes, created_at, updated_at
		FROM mail_accounts WHERE user_id = ?`, userID).
		Scan(&a.ID, &a.UserID, &a.Email, &a.Host, &a.Port, &a.Username, &a.EncryptedPassword, &a.UseTLS, &a.SMTPHost, &a.SMTPPort, &a.SMTPUsername, &a.EncryptedSMTPPassword, &a.SMTPUseTLS, &a.Mailbox, &a.SyncIntervalMinutes, &created, &updated)
	a.CreatedAt = unixTime(created)
	a.UpdatedAt = unixTime(updated)
	a.applySMTPDefaults()
	return a, err
}

func (s *Store) ListAccounts(ctx context.Context) ([]MailAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, email, host, port, username, encrypted_password, use_tls, smtp_host, smtp_port, smtp_username, encrypted_smtp_password, smtp_use_tls, mailbox, sync_interval_minutes, created_at, updated_at FROM mail_accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []MailAccount
	for rows.Next() {
		var a MailAccount
		var created, updated int64
		if err := rows.Scan(&a.ID, &a.UserID, &a.Email, &a.Host, &a.Port, &a.Username, &a.EncryptedPassword, &a.UseTLS, &a.SMTPHost, &a.SMTPPort, &a.SMTPUsername, &a.EncryptedSMTPPassword, &a.SMTPUseTLS, &a.Mailbox, &a.SyncIntervalMinutes, &created, &updated); err != nil {
			return nil, err
		}
		a.CreatedAt = unixTime(created)
		a.UpdatedAt = unixTime(updated)
		a.applySMTPDefaults()
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func (a *MailAccount) applySMTPDefaults() {
	if strings.TrimSpace(a.SMTPHost) == "" {
		a.SMTPHost = a.Host
	}
	if a.SMTPPort == 0 {
		a.SMTPPort = 587
	}
	if strings.TrimSpace(a.SMTPUsername) == "" {
		a.SMTPUsername = a.Username
	}
	if a.EncryptedSMTPPassword == "" {
		a.EncryptedSMTPPassword = a.EncryptedPassword
	}
}

func (s *Store) GetOrCreateMailbox(ctx context.Context, userID, accountID int64, name string) (Mailbox, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "INBOX"
	}
	ts := nowUnix()
	syncMode := defaultMailboxSyncMode(name)
	role := defaultMailboxRole(name)
	icon := defaultMailboxIcon(name, role)
	showInAllMail := defaultMailboxShowInAllMail(role)
	_, err := s.db.ExecContext(ctx, `INSERT INTO mailboxes (user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, 1, ?, ?)
		ON CONFLICT(user_id, account_id, name) DO NOTHING`, userID, accountID, name, syncMode, role, icon, boolInt(showInAllMail), ts, ts)
	if err != nil {
		return Mailbox{}, err
	}
	return s.GetMailbox(ctx, userID, accountID, name)
}

func (s *Store) NextUIDForMailbox(ctx context.Context, userID, mailboxID int64) (uint32, error) {
	var next uint32
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(uid), 0) + 1 FROM messages WHERE user_id = ? AND mailbox_id = ?`, userID, mailboxID).Scan(&next)
	if next == 0 {
		next = 1
	}
	return next, err
}

func (s *Store) GetMailbox(ctx context.Context, userID, accountID int64, name string) (Mailbox, error) {
	var m Mailbox
	var created, updated, statusChecked int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, uidvalidity, last_uid,
			remote_message_count, remote_unread_count, remote_uid_next, status_checked_at, created_at, updated_at
		FROM mailboxes WHERE user_id = ? AND account_id = ? AND name = ?`, userID, accountID, name).
		Scan(&m.ID, &m.UserID, &m.AccountID, &m.Name, &m.SyncMode, &m.Role, &m.Icon, &m.ShowInSidebar, &m.ShowInAllMail, &m.IncludeInSearch, &m.UIDValidity, &m.LastUID,
			&m.RemoteMessageCount, &m.RemoteUnreadCount, &m.RemoteUIDNext, &statusChecked, &created, &updated)
	m.SyncMode = normalizeSyncMode(m.SyncMode)
	m.Role = normalizeMailboxRole(m.Role)
	m.Icon = normalizeMailboxIcon(m.Icon, m.Name, m.Role)
	m.StatusCheckedAt = unixTime(statusChecked)
	m.CreatedAt = unixTime(created)
	m.UpdatedAt = unixTime(updated)
	return m, err
}

func (s *Store) GetMailboxForUser(ctx context.Context, userID, mailboxID int64) (Mailbox, error) {
	var m Mailbox
	var created, updated, statusChecked int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, uidvalidity, last_uid,
			remote_message_count, remote_unread_count, remote_uid_next, status_checked_at, created_at, updated_at
		FROM mailboxes WHERE user_id = ? AND id = ?`, userID, mailboxID).
		Scan(&m.ID, &m.UserID, &m.AccountID, &m.Name, &m.SyncMode, &m.Role, &m.Icon, &m.ShowInSidebar, &m.ShowInAllMail, &m.IncludeInSearch, &m.UIDValidity, &m.LastUID,
			&m.RemoteMessageCount, &m.RemoteUnreadCount, &m.RemoteUIDNext, &statusChecked, &created, &updated)
	m.SyncMode = normalizeSyncMode(m.SyncMode)
	m.Role = normalizeMailboxRole(m.Role)
	m.Icon = normalizeMailboxIcon(m.Icon, m.Name, m.Role)
	m.StatusCheckedAt = unixTime(statusChecked)
	m.CreatedAt = unixTime(created)
	m.UpdatedAt = unixTime(updated)
	return m, err
}

func (s *Store) ListMailboxesForUser(ctx context.Context, userID int64) ([]MailboxSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mb.id, mb.user_id, mb.account_id, mb.name, mb.sync_mode, mb.role, mb.icon,
			mb.show_in_sidebar, mb.show_in_all_mail, mb.include_in_search, mb.uidvalidity, mb.last_uid, mb.created_at, mb.updated_at,
			mb.remote_message_count, mb.remote_unread_count, mb.remote_uid_next, mb.status_checked_at,
			ma.email,
			count(m.id),
			COALESCE(sum(CASE WHEN m.is_read = 0 THEN 1 ELSE 0 END), 0)
		FROM mailboxes mb
		JOIN mail_accounts ma ON ma.id = mb.account_id AND ma.user_id = mb.user_id
		LEFT JOIN messages m ON m.user_id = mb.user_id AND m.mailbox_id = mb.id
		WHERE mb.user_id = ?
		GROUP BY mb.id
		ORDER BY CASE WHEN mb.role = 'inbox' OR lower(mb.name) = 'inbox' THEN 0 ELSE 1 END, ma.email, lower(mb.name)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailboxSummary
	for rows.Next() {
		var ms MailboxSummary
		var created, updated, statusChecked int64
		var localMessages, localUnread int
		if err := rows.Scan(&ms.ID, &ms.UserID, &ms.AccountID, &ms.Name, &ms.SyncMode, &ms.Role, &ms.Icon,
			&ms.ShowInSidebar, &ms.ShowInAllMail, &ms.IncludeInSearch, &ms.UIDValidity, &ms.LastUID, &created, &updated,
			&ms.RemoteMessageCount, &ms.RemoteUnreadCount, &ms.RemoteUIDNext, &statusChecked, &ms.AccountEmail, &localMessages, &localUnread); err != nil {
			return nil, err
		}
		ms.SyncMode = normalizeSyncMode(ms.SyncMode)
		ms.Role = normalizeMailboxRole(ms.Role)
		ms.Icon = normalizeMailboxIcon(ms.Icon, ms.Name, ms.Role)
		ms.StatusCheckedAt = unixTime(statusChecked)
		ms.CreatedAt = unixTime(created)
		ms.UpdatedAt = unixTime(updated)
		ms.MessageCount = localMessages
		ms.UnreadCount = localUnread
		if statusChecked > 0 {
			ms.MessageCount = ms.RemoteMessageCount
			ms.UnreadCount = ms.RemoteUnreadCount
		}
		ms.SyncPercent = mailboxSyncPercent(ms.LastUID, ms.RemoteUIDNext, ms.MessageCount)
		out = append(out, ms)
	}
	return out, rows.Err()
}

func (s *Store) LastUIDs(ctx context.Context, userID, accountID int64) (map[string]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, last_uid FROM mailboxes WHERE user_id = ? AND account_id = ?`, userID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]uint32)
	for rows.Next() {
		var name string
		var uid uint32
		if err := rows.Scan(&name, &uid); err != nil {
			return nil, err
		}
		out[name] = uid
	}
	return out, rows.Err()
}

func (s *Store) UpdateMailboxSyncMode(ctx context.Context, userID, mailboxID int64, mode string) error {
	mode = normalizeSyncMode(mode)
	res, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET sync_mode = ?, updated_at = ? WHERE user_id = ? AND id = ?`, mode, nowUnix(), userID, mailboxID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateMailboxSettings(ctx context.Context, userID, mailboxID int64, settings MailboxSettings) error {
	settings.SyncMode = normalizeSyncMode(settings.SyncMode)
	settings.Role = normalizeMailboxRole(settings.Role)
	settings.Icon = normalizeMailboxIcon(settings.Icon, "", settings.Role)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if settings.Role == "inbox" || settings.Role == "trash" {
		if _, err := tx.ExecContext(ctx, `UPDATE mailboxes SET role = '', updated_at = ?
			WHERE user_id = ? AND account_id = (SELECT account_id FROM mailboxes WHERE user_id = ? AND id = ?) AND role = ? AND id <> ?`,
			nowUnix(), userID, userID, mailboxID, settings.Role, mailboxID); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE mailboxes
		SET sync_mode = ?, role = ?, icon = ?, show_in_sidebar = ?, show_in_all_mail = ?, include_in_search = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`,
		settings.SyncMode, settings.Role, settings.Icon, boolInt(settings.ShowInSidebar), boolInt(settings.ShowInAllMail), boolInt(settings.IncludeInSearch), nowUnix(), userID, mailboxID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) EffectiveMailboxSyncMode(ctx context.Context, userID, accountID int64, mailbox Mailbox) (string, error) {
	mode := normalizeSyncMode(mailbox.SyncMode)
	if mode != "inherit" {
		return mode, nil
	}
	for _, parent := range mailboxParentNames(mailbox.Name) {
		mb, err := s.GetMailbox(ctx, userID, accountID, parent)
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		parentMode := normalizeSyncMode(mb.SyncMode)
		if parentMode != "inherit" {
			return parentMode, nil
		}
	}
	return "auto", nil
}

func (s *Store) ListMessagesForMailboxIndex(ctx context.Context, userID, mailboxID int64, limit int, afterID int64) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND mailbox_id = ? AND id > ? ORDER BY id LIMIT ?`, userID, mailboxID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func normalizeSyncMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "inherit":
		return "inherit"
	case "manual":
		return "manual"
	case "never":
		return "never"
	default:
		return "auto"
	}
}

func normalizeMailboxRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "inbox":
		return "inbox"
	case "trash":
		return "trash"
	default:
		return ""
	}
}

func normalizeMailboxIcon(icon string, name string, role string) string {
	icon = strings.ToLower(strings.TrimSpace(icon))
	switch icon {
	case "inbox", "delete", "folder", "folder_open", "archive", "send", "draft", "sell", "shopping_bag", "label", "star", "report", "block", "mail":
		return icon
	}
	return defaultMailboxIcon(name, role)
}

func defaultMailboxSyncMode(name string) string {
	if len(mailboxParentNames(name)) > 0 {
		return "inherit"
	}
	return "auto"
}

func defaultMailboxRole(name string) string {
	clean := strings.ToLower(strings.TrimSpace(name))
	switch clean {
	case "inbox":
		return "inbox"
	case "trash", "deleted", "deleted items", "[gmail]/trash":
		return "trash"
	default:
		return ""
	}
}

func defaultMailboxIcon(name string, role string) string {
	switch normalizeMailboxRole(role) {
	case "inbox":
		return "inbox"
	case "trash":
		return "delete"
	}
	clean := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(clean, "archive"):
		return "archive"
	case strings.Contains(clean, "sent"):
		return "send"
	case strings.Contains(clean, "draft"):
		return "draft"
	case strings.Contains(clean, "spam"), strings.Contains(clean, "junk"):
		return "report"
	default:
		return "folder"
	}
}

func defaultMailboxShowInAllMail(role string) bool {
	return normalizeMailboxRole(role) != "trash"
}

func mailboxSyncPercent(lastUID uint32, remoteUIDNext uint32, messageCount int) int {
	if remoteUIDNext > 1 {
		total := remoteUIDNext - 1
		if lastUID >= total {
			return 100
		}
		return int((uint64(lastUID) * 100) / uint64(total))
	}
	if messageCount > 0 {
		return 100
	}
	return 0
}

func mailboxParentNames(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var parents []string
	for i := len(name) - 1; i > 0; i-- {
		switch name[i] {
		case '.', '/', '\\':
			parent := strings.TrimSpace(name[:i])
			if parent != "" {
				parents = append(parents, parent)
			}
		}
	}
	return parents
}

func (s *Store) UpdateMailboxLastUID(ctx context.Context, userID, mailboxID int64, uid uint32) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailboxes SET last_uid = CASE WHEN last_uid < ? THEN ? ELSE last_uid END, updated_at = ?
		WHERE id = ? AND user_id = ?`, uid, uid, nowUnix(), mailboxID, userID)
	return err
}

func (s *Store) UpdateMailboxRemoteStatus(ctx context.Context, userID, mailboxID int64, messageCount, unreadCount int, uidNext uint32, uidValidity uint32) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mailboxes
		SET remote_message_count = ?, remote_unread_count = ?, remote_uid_next = ?, uidvalidity = ?, status_checked_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		messageCount, unreadCount, uidNext, uidValidity, nowUnix(), nowUnix(), mailboxID, userID)
	return err
}

func (s *Store) MessageExistsByUID(ctx context.Context, userID, accountID, mailboxID int64, uid uint32) (bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ?`,
		userID, accountID, mailboxID, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) UpdateMessageReadByUID(ctx context.Context, userID, accountID, mailboxID int64, uid uint32, isRead bool, pending bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET is_read = ?, read_sync_pending = ?, updated_at = ?
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ?`,
		boolInt(isRead), boolInt(pending), nowUnix(), userID, accountID, mailboxID, uid)
	return err
}

func (s *Store) MarkMessageReadForUser(ctx context.Context, userID, messageID int64, isRead bool, pending bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET is_read = ?, read_sync_pending = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(isRead), boolInt(pending), nowUnix(), userID, messageID)
	return err
}

func (s *Store) ClearReadSyncPending(ctx context.Context, userID, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET read_sync_pending = 0, updated_at = ? WHERE user_id = ? AND id = ?`,
		nowUnix(), userID, messageID)
	return err
}

func (s *Store) MarkMessageStarredForUser(ctx context.Context, userID, messageID int64, isStarred bool, pending bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET is_starred = ?, star_sync_pending = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(isStarred), boolInt(pending), nowUnix(), userID, messageID)
	return err
}

func (s *Store) ClearStarSyncPending(ctx context.Context, userID, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET star_sync_pending = 0, updated_at = ? WHERE user_id = ? AND id = ?`,
		nowUnix(), userID, messageID)
	return err
}

func (s *Store) UpdateMailboxStarFlags(ctx context.Context, userID, accountID, mailboxID int64, flaggedUIDs []uint32) ([]int64, error) {
	flagged := make(map[uint32]bool, len(flaggedUIDs))
	for _, uid := range flaggedUIDs {
		flagged[uid] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, uid, is_starred FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND star_sync_pending = 0`,
		userID, accountID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []struct {
		ID        int64
		UID       uint32
		IsStarred bool
	}
	for rows.Next() {
		var id int64
		var uid uint32
		var current bool
		if err := rows.Scan(&id, &uid, &current); err != nil {
			return nil, err
		}
		next := flagged[uid]
		if current != next {
			updates = append(updates, struct {
				ID        int64
				UID       uint32
				IsStarred bool
			}{ID: id, UID: uid, IsStarred: next})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUnix()
	changed := make([]int64, 0, len(updates))
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET is_starred = ?, updated_at = ?
			WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ? AND star_sync_pending = 0`,
			boolInt(update.IsStarred), now, userID, accountID, mailboxID, update.UID); err != nil {
			return nil, err
		}
		changed = append(changed, update.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

func (s *Store) UpdateMailboxReadFlags(ctx context.Context, userID, accountID, mailboxID int64, seenUIDs []uint32) ([]int64, error) {
	seen := make(map[uint32]bool, len(seenUIDs))
	for _, uid := range seenUIDs {
		seen[uid] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, uid, is_read FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND read_sync_pending = 0`,
		userID, accountID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []struct {
		ID     int64
		UID    uint32
		IsRead bool
	}
	for rows.Next() {
		var id int64
		var uid uint32
		var current bool
		if err := rows.Scan(&id, &uid, &current); err != nil {
			return nil, err
		}
		next := seen[uid]
		if current != next {
			updates = append(updates, struct {
				ID     int64
				UID    uint32
				IsRead bool
			}{ID: id, UID: uid, IsRead: next})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUnix()
	changed := make([]int64, 0, len(updates))
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET is_read = ?, updated_at = ?
			WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ? AND read_sync_pending = 0`,
			boolInt(update.IsRead), now, userID, accountID, mailboxID, update.UID); err != nil {
			return nil, err
		}
		changed = append(changed, update.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

type CreateMessage struct {
	UserID           int64
	AccountID        int64
	MailboxID        int64
	BlobID           int64
	MessageIDHeader  string
	InReplyTo        string
	ReferencesHeader string
	ThreadKey        string
	Subject          string
	LanguageCode     string
	FromAddr         string
	ToAddr           string
	CCAddr           string
	Date             time.Time
	InternalDate     time.Time
	UID              uint32
	Size             int64
	BlobPath         string
	BodyText         string
	BodyHTML         string
	IsRead           bool
	IsStarred        bool
	HasAttachments   bool
}

func (s *Store) CreateMessage(ctx context.Context, m CreateMessage) (MessageRecord, error) {
	ts := nowUnix()
	if strings.TrimSpace(m.ThreadKey) == "" {
		m.ThreadKey = ThreadKey(m.MessageIDHeader, m.InReplyTo, m.ReferencesHeader, m.Subject)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO messages
			(user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, thread_headers_checked_at, subject, language_code, from_addr, to_addr, cc_addr, date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, is_starred, has_attachments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.UserID, m.AccountID, m.MailboxID, m.BlobID, m.MessageIDHeader, m.InReplyTo, m.ReferencesHeader, m.ThreadKey, ts, m.Subject, strings.ToLower(strings.TrimSpace(m.LanguageCode)), m.FromAddr, m.ToAddr, m.CCAddr,
		m.Date.UTC().Unix(), m.InternalDate.UTC().Unix(), m.UID, m.Size, m.BlobPath, m.BodyText, m.BodyHTML, boolInt(m.IsRead), boolInt(m.IsStarred), boolInt(m.HasAttachments), ts, ts)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: messages.user_id, messages.account_id, messages.mailbox_id, messages.uid") {
			return s.GetMessageByUID(ctx, m.UserID, m.AccountID, m.MailboxID, m.UID)
		}
		return MessageRecord{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MessageRecord{}, err
	}
	return s.GetMessageForUser(ctx, m.UserID, id)
}

func (s *Store) GetMessageByUID(ctx context.Context, userID, accountID, mailboxID int64, uid uint32) (MessageRecord, error) {
	var m MessageRecord
	var dateUnix, internalUnix, indexedAt, created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ?`, userID, accountID, mailboxID, uid).
		Scan(&m.ID, &m.UserID, &m.AccountID, &m.MailboxID, &m.BlobID, &m.MessageIDHeader, &m.InReplyTo, &m.ReferencesHeader, &m.ThreadKey, &m.Subject, &m.LanguageCode, &m.FromAddr, &m.ToAddr, &m.CCAddr,
			&dateUnix, &internalUnix, &m.UID, &m.Size, &m.BlobPath, &m.BodyText, &m.BodyHTML, &m.IsRead, &m.ReadSyncPending, &m.IsStarred, &m.StarSyncPending, &m.HasAttachments, &indexedAt, &created, &updated)
	m.Date = unixTime(dateUnix)
	m.InternalDate = unixTime(internalUnix)
	m.AttachmentIndexedAt = unixTime(indexedAt)
	m.CreatedAt = unixTime(created)
	m.UpdatedAt = unixTime(updated)
	return m, err
}

func (s *Store) CreateLocation(ctx context.Context, userID, messageID, mailboxID int64, uid uint32) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO locations (user_id, message_id, mailbox_id, uid, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, messageID, mailboxID, uid, nowUnix())
	return err
}

func (s *Store) GetMessageForUser(ctx context.Context, userID, id int64) (MessageRecord, error) {
	var m MessageRecord
	var dateUnix, internalUnix, indexedAt, created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&m.ID, &m.UserID, &m.AccountID, &m.MailboxID, &m.BlobID, &m.MessageIDHeader, &m.InReplyTo, &m.ReferencesHeader, &m.ThreadKey, &m.Subject, &m.LanguageCode, &m.FromAddr, &m.ToAddr, &m.CCAddr,
			&dateUnix, &internalUnix, &m.UID, &m.Size, &m.BlobPath, &m.BodyText, &m.BodyHTML, &m.IsRead, &m.ReadSyncPending, &m.IsStarred, &m.StarSyncPending, &m.HasAttachments, &indexedAt, &created, &updated)
	m.Date = unixTime(dateUnix)
	m.InternalDate = unixTime(internalUnix)
	m.AttachmentIndexedAt = unixTime(indexedAt)
	m.CreatedAt = unixTime(created)
	m.UpdatedAt = unixTime(updated)
	return m, err
}

func (s *Store) GetMessageByBlobIDForUser(ctx context.Context, userID, blobID int64) (MessageRecord, error) {
	var m MessageRecord
	var dateUnix, internalUnix, indexedAt, created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND blob_id = ?`, userID, blobID).
		Scan(&m.ID, &m.UserID, &m.AccountID, &m.MailboxID, &m.BlobID, &m.MessageIDHeader, &m.InReplyTo, &m.ReferencesHeader, &m.ThreadKey, &m.Subject, &m.LanguageCode, &m.FromAddr, &m.ToAddr, &m.CCAddr,
			&dateUnix, &internalUnix, &m.UID, &m.Size, &m.BlobPath, &m.BodyText, &m.BodyHTML, &m.IsRead, &m.ReadSyncPending, &m.IsStarred, &m.StarSyncPending, &m.HasAttachments, &indexedAt, &created, &updated)
	m.Date = unixTime(dateUnix)
	m.InternalDate = unixTime(internalUnix)
	m.AttachmentIndexedAt = unixTime(indexedAt)
	m.CreatedAt = unixTime(created)
	m.UpdatedAt = unixTime(updated)
	return m, err
}

func (s *Store) DeleteMessageForUser(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMessagesMissingUIDs(ctx context.Context, userID, accountID, mailboxID int64, remoteUIDs []uint32) ([]MessageRecord, error) {
	remote := make(map[uint32]bool, len(remoteUIDs))
	for _, uid := range remoteUIDs {
		remote[uid] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, userID, accountID, mailboxID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	local, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	var stale []MessageRecord
	for _, msg := range local {
		if !remote[msg.UID] {
			stale = append(stale, msg)
		}
	}
	if len(stale) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM messages WHERE user_id = ? AND id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	for _, msg := range stale {
		if _, err := stmt.ExecContext(ctx, userID, msg.ID); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stale, nil
}

func MessageBodyPreview(value string, limit int) string {
	if limit <= 0 {
		limit = DefaultMessageBodyPreviewBytes
	}
	preview := strings.Join(strings.Fields(value), " ")
	if len(preview) <= limit {
		return preview
	}
	suffix := " ..."
	cut := limit - len(suffix)
	for cut > 0 && !utf8.RuneStart(preview[cut]) {
		cut--
	}
	if cut <= 0 {
		return ""
	}
	return strings.TrimSpace(preview[:cut]) + suffix
}

func (s *Store) CompactMessageBodiesBefore(ctx context.Context, cutoff time.Time, previewLimit, limit int) (int, error) {
	if previewLimit <= 0 {
		previewLimit = DefaultMessageBodyPreviewBytes
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, body_text FROM messages
		WHERE date_unix < ? AND (body_html != '' OR length(body_text) > ?)
		ORDER BY date_unix, id LIMIT ?`, cutoff.UTC().Unix(), previewLimit, limit)
	if err != nil {
		return 0, err
	}
	type row struct {
		id       int64
		bodyText string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.bodyText); err != nil {
			_ = rows.Close()
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE messages SET body_text = ?, body_html = '', updated_at = ? WHERE id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	now := nowUnix()
	for _, r := range pending {
		if _, err := stmt.ExecContext(ctx, MessageBodyPreview(r.bodyText, previewLimit), now, r.id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pending), nil
}

func (s *Store) ListMessagesWithPrunableBlobs(ctx context.Context, cutoff time.Time, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE blob_path != '' AND date_unix < ? ORDER BY date_unix, id LIMIT ?`, cutoff.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) MarkMessageBlobPruned(ctx context.Context, userID, messageID, blobID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE messages SET blob_path = '', updated_at = ? WHERE user_id = ? AND id = ? AND blob_id = ?`,
		nowUnix(), userID, messageID, blobID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET kind = 'message-remote', size = 0 WHERE user_id = ? AND id = ?`, userID, blobID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) ListMessagesForUser(ctx context.Context, userID int64, limit, offset int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? ORDER BY date_unix DESC, id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListMessagesForMailbox(ctx context.Context, userID, mailboxID int64, limit, offset int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND mailbox_id = ? ORDER BY date_unix DESC, id DESC LIMIT ? OFFSET ?`, userID, mailboxID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) CountMessagesForMailbox(ctx context.Context, userID, mailboxID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ? AND mailbox_id = ?`, userID, mailboxID).Scan(&n)
	return n, err
}

func (s *Store) ListLatestThreadMessagesForUser(ctx context.Context, userID int64, limit, offset int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `WITH keyed AS (
			SELECT m.id, COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id) AS thread_group, m.date_unix
			FROM messages m
			JOIN mailboxes mb ON mb.id = m.mailbox_id AND mb.user_id = m.user_id
			WHERE m.user_id = ? AND mb.show_in_all_mail = 1
		), ranked AS (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY thread_group ORDER BY date_unix DESC, id DESC) AS rn,
				MAX(date_unix) OVER (PARTITION BY thread_group) AS latest_date
			FROM keyed
		)
		SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM ranked r JOIN messages m ON m.id = r.id
		WHERE r.rn = 1
		ORDER BY r.latest_date DESC, m.id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListLatestThreadMessagesForMailbox(ctx context.Context, userID, mailboxID int64, limit, offset int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `WITH keyed AS (
			SELECT id, COALESCE(NULLIF(thread_key, ''), 'id:' || id) AS thread_group, date_unix
			FROM messages WHERE user_id = ? AND mailbox_id = ?
		), ranked AS (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY thread_group ORDER BY date_unix DESC, id DESC) AS rn,
				MAX(date_unix) OVER (PARTITION BY thread_group) AS latest_date
			FROM keyed
		)
		SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM ranked r JOIN messages m ON m.id = r.id
		WHERE r.rn = 1
		ORDER BY r.latest_date DESC, m.id DESC LIMIT ? OFFSET ?`, userID, mailboxID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListMessagesByIDsForUser(ctx context.Context, userID int64, ids []int64) ([]MessageRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	messages := make([]MessageRecord, 0, len(ids))
	for _, id := range ids {
		m, err := s.GetMessageForUser(ctx, userID, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, nil
}

func (s *Store) ListThreadMessagesForUser(ctx context.Context, userID int64, msg MessageRecord) ([]MessageRecord, error) {
	key := strings.TrimSpace(msg.ThreadKey)
	if key == "" {
		key = ThreadKey(msg.MessageIDHeader, msg.InReplyTo, msg.ReferencesHeader, msg.Subject)
	}
	if key == "" {
		return []MessageRecord{msg}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND thread_key = ? ORDER BY date_unix ASC, id ASC`, userID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListThreadMessagesByKeysForUser(ctx context.Context, userID int64, keys []string) (map[string][]MessageRecord, error) {
	out := make(map[string][]MessageRecord, len(keys))
	seen := map[string]bool{}
	unique := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, key)
	}
	const chunkSize = 200
	for start := 0; start < len(unique); start += chunkSize {
		end := start + chunkSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, userID)
		for i, key := range chunk {
			placeholders[i] = "?"
			args = append(args, key)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND thread_key IN (`+strings.Join(placeholders, ",")+`) ORDER BY thread_key ASC, date_unix ASC, id ASC`, args...)
		if err != nil {
			return nil, err
		}
		messages, err := scanMessages(rows)
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		for _, msg := range messages {
			out[msg.ThreadKey] = append(out[msg.ThreadKey], msg)
		}
	}
	return out, nil
}

func (s *Store) BackfillThreadKeys(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id_header, in_reply_to, references_header, subject
		FROM messages WHERE thread_key = '' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		id         int64
		messageID  string
		inReplyTo  string
		references string
		subject    string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.messageID, &r.inReplyTo, &r.references, &r.subject); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range pending {
		key := ThreadKey(r.messageID, r.inReplyTo, r.references, r.subject)
		if key == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages SET thread_key = ?, updated_at = ? WHERE id = ? AND thread_key = ''`, key, nowUnix(), r.id); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

func (s *Store) BackfillThreadHeadersFromBlobs(ctx context.Context, dataDir string, limit int) (int, int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, blob_path, message_id_header, in_reply_to, references_header, subject, thread_key
		FROM messages WHERE thread_headers_checked_at = 0 AND blob_path != '' ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	type row struct {
		id         int64
		blobPath   string
		messageID  string
		inReplyTo  string
		references string
		subject    string
		threadKey  string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.blobPath, &r.messageID, &r.inReplyTo, &r.references, &r.subject, &r.threadKey); err != nil {
			return 0, 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	checked, updated := 0, 0
	for _, r := range pending {
		messageID, inReplyTo, references := strings.TrimSpace(r.messageID), strings.TrimSpace(r.inReplyTo), strings.TrimSpace(r.references)
		hMessageID, hInReplyTo, hReferences, err := readThreadHeaders(filepath.Join(dataDir, filepath.Clean(r.blobPath)))
		if err == nil {
			if hMessageID != "" {
				messageID = hMessageID
			}
			if hInReplyTo != "" {
				inReplyTo = hInReplyTo
			}
			if hReferences != "" {
				references = hReferences
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return checked, updated, err
		}

		key := ThreadKey(messageID, inReplyTo, references, r.subject)
		if key == "" {
			key = strings.TrimSpace(r.threadKey)
		}
		changed := messageID != r.messageID || inReplyTo != r.inReplyTo || references != r.references || key != r.threadKey
		if _, err := s.db.ExecContext(ctx, `UPDATE messages
			SET message_id_header = ?, in_reply_to = ?, references_header = ?, thread_key = ?, thread_headers_checked_at = ?, updated_at = ?
			WHERE id = ?`,
			messageID, inReplyTo, references, key, nowUnix(), nowUnix(), r.id); err != nil {
			return checked, updated, err
		}
		checked++
		if changed {
			updated++
		}
	}
	return checked, updated, nil
}

func readThreadHeaders(path string) (string, string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	if prefix, err := br.Peek(5); err == nil && string(prefix) == "From " {
		_, _ = br.ReadString('\n')
	}
	headers, err := textproto.NewReader(br).ReadMIMEHeader()
	if err != nil {
		return "", "", "", nil
	}
	return strings.TrimSpace(headers.Get("Message-Id")), strings.TrimSpace(headers.Get("In-Reply-To")), strings.TrimSpace(headers.Get("References")), nil
}

func (s *Store) ListReadSenderStatsForUser(ctx context.Context, userID int64, limit int) ([]SenderReadStat, error) {
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	rows, err := s.db.QueryContext(ctx, `SELECT from_addr, is_read FROM messages WHERE user_id = ? AND from_addr != ''`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statsBySender := map[string]*SenderReadStat{}
	for rows.Next() {
		var from string
		var isRead bool
		if err := rows.Scan(&from, &isRead); err != nil {
			return nil, err
		}
		sender := SenderIdentity(from)
		if sender == "" {
			continue
		}
		stat := statsBySender[sender]
		if stat == nil {
			stat = &SenderReadStat{Sender: sender}
			statsBySender[sender] = stat
		}
		stat.TotalCount++
		if isRead {
			stat.ReadCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stats := make([]SenderReadStat, 0, len(statsBySender))
	for _, stat := range statsBySender {
		if stat.ReadCount == 0 {
			continue
		}
		ratio := float64(stat.ReadCount) / float64(stat.TotalCount)
		boost := 0.6 + ratio*1.4 + float64(stat.ReadCount)/8
		if boost > 8 {
			boost = 8
		}
		stat.Boost = boost
		stats = append(stats, *stat)
	}
	sortSenderStats(stats)
	if len(stats) > limit {
		stats = stats[:limit]
	}
	return stats, nil
}

func (s *Store) TrustImageSender(ctx context.Context, userID int64, sender string) error {
	sender = SenderIdentity(sender)
	if userID == 0 || sender == "" {
		return errors.New("image sender preference fields are incomplete")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO trusted_image_senders (user_id, sender, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id, sender) DO NOTHING`, userID, sender, nowUnix())
	return err
}

func (s *Store) IsImageSenderTrusted(ctx context.Context, userID int64, sender string) (bool, error) {
	sender = SenderIdentity(sender)
	if userID == 0 || sender == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM trusted_image_senders WHERE user_id = ? AND sender = ?`, userID, sender).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

var defaultRemoteImageBlockPatterns = []string{
	`(?i)https?://[^"'\s>]*(klaviyo|klclick|trk\.klclick)[^"'\s>]*(/open|/track|/event|pixel)`,
	`(?i)https?://[^"'\s>]*(list-manage\.com|mailchimp\.com|mandrillapp\.com)[^"'\s>]*(/track|/open|/mctrack|/pixel)`,
	`(?i)https?://[^"'\s>]*/(?:open\.php|track/open|pixel|beacon|1x1|transparent)[^"'\s>]*`,
}

func (s *Store) seedRemoteImageBlockRules(ctx context.Context) error {
	ts := nowUnix()
	for _, pattern := range defaultRemoteImageBlockPatterns {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO remote_image_block_rules (pattern, enabled, created_at, updated_at)
			VALUES (?, 1, ?, ?)
			ON CONFLICT(pattern) DO NOTHING`, pattern, ts, ts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListRemoteImageBlockRules(ctx context.Context) ([]RemoteImageBlockRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, pattern, enabled, created_at, updated_at FROM remote_image_block_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteImageBlockRule
	for rows.Next() {
		var rule RemoteImageBlockRule
		var enabled int
		var created, updated int64
		if err := rows.Scan(&rule.ID, &rule.Pattern, &enabled, &created, &updated); err != nil {
			return nil, err
		}
		rule.Enabled = enabled != 0
		rule.CreatedAt = unixTime(created)
		rule.UpdatedAt = unixTime(updated)
		out = append(out, rule)
	}
	return out, rows.Err()
}

func (s *Store) ListRemoteImageBlockPatterns(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pattern FROM remote_image_block_rules WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return nil, err
		}
		if strings.TrimSpace(pattern) != "" {
			out = append(out, pattern)
		}
	}
	return out, rows.Err()
}

func (s *Store) ReplaceRemoteImageBlockRules(ctx context.Context, patterns []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM remote_image_block_rules`); err != nil {
		return err
	}
	ts := nowUnix()
	seen := map[string]bool{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || seen[pattern] {
			continue
		}
		seen[pattern] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_image_block_rules (pattern, enabled, created_at, updated_at) VALUES (?, 1, ?, ?)`, pattern, ts, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RecordOneClickUnsubscribeSend(ctx context.Context, userID, messageID int64, sender, unsubscribeURL string, sentAt time.Time) error {
	sender = SenderIdentity(sender)
	unsubscribeURL = strings.TrimSpace(unsubscribeURL)
	if userID == 0 || messageID == 0 || unsubscribeURL == "" {
		return errors.New("unsubscribe send fields are incomplete")
	}
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	sentUnix := sentAt.UTC().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO one_click_unsubscribe_sends
			(user_id, message_id, sender, unsubscribe_url, sent_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, message_id, unsubscribe_url) DO UPDATE SET
			sender = excluded.sender,
			sent_at = excluded.sent_at`,
		userID, messageID, sender, unsubscribeURL, sentUnix, sentUnix)
	return err
}

func (s *Store) LatestOneClickUnsubscribeSend(ctx context.Context, userID, messageID int64, unsubscribeURL string, since time.Time) (OneClickUnsubscribeSend, error) {
	unsubscribeURL = strings.TrimSpace(unsubscribeURL)
	if userID == 0 || (messageID == 0 && unsubscribeURL == "") {
		return OneClickUnsubscribeSend{}, ErrNotFound
	}
	sinceUnix := int64(0)
	if !since.IsZero() {
		sinceUnix = since.UTC().Unix()
	}
	var send OneClickUnsubscribeSend
	var sentAt, createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, message_id, sender, unsubscribe_url, sent_at, created_at
		FROM one_click_unsubscribe_sends
		WHERE user_id = ? AND sent_at >= ? AND (message_id = ? OR unsubscribe_url = ?)
		ORDER BY sent_at DESC, id DESC LIMIT 1`,
		userID, sinceUnix, messageID, unsubscribeURL).
		Scan(&send.ID, &send.UserID, &send.MessageID, &send.Sender, &send.UnsubscribeURL, &sentAt, &createdAt)
	send.SentAt = unixTime(sentAt)
	send.CreatedAt = unixTime(createdAt)
	return send, err
}

func (s *Store) ListMessagesNeedingAttachmentIndex(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND attachment_indexed_at = 0 ORDER BY id LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListMessagesWithReadSyncPending(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND read_sync_pending = 1 ORDER BY updated_at LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListMessagesWithStarSyncPending(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND star_sync_pending = 1 ORDER BY updated_at LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) MarkMessageAttachmentIndexed(ctx context.Context, userID, messageID int64, hasAttachments bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET has_attachments = ?, attachment_indexed_at = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(hasAttachments), nowUnix(), nowUnix(), userID, messageID)
	return err
}

func (s *Store) UpdateMessageLanguage(ctx context.Context, userID, messageID int64, languageCode string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET language_code = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, strings.ToLower(strings.TrimSpace(languageCode)), nowUnix(), userID, messageID)
	return err
}

func scanMessages(rows *sql.Rows) ([]MessageRecord, error) {
	var out []MessageRecord
	for rows.Next() {
		var m MessageRecord
		var dateUnix, internalUnix, indexedAt, created, updated int64
		if err := rows.Scan(&m.ID, &m.UserID, &m.AccountID, &m.MailboxID, &m.BlobID, &m.MessageIDHeader, &m.InReplyTo, &m.ReferencesHeader, &m.ThreadKey, &m.Subject, &m.LanguageCode, &m.FromAddr, &m.ToAddr, &m.CCAddr,
			&dateUnix, &internalUnix, &m.UID, &m.Size, &m.BlobPath, &m.BodyText, &m.BodyHTML, &m.IsRead, &m.ReadSyncPending, &m.IsStarred, &m.StarSyncPending, &m.HasAttachments, &indexedAt, &created, &updated); err != nil {
			return nil, err
		}
		m.Date = unixTime(dateUnix)
		m.InternalDate = unixTime(internalUnix)
		m.AttachmentIndexedAt = unixTime(indexedAt)
		m.CreatedAt = unixTime(created)
		m.UpdatedAt = unixTime(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CreateBlob(ctx context.Context, b BlobRecord) (BlobRecord, error) {
	ts := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO blobs (user_id, kind, path, sha256, size, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, path) DO UPDATE SET
			kind = excluded.kind,
			sha256 = excluded.sha256,
			size = excluded.size`,
		b.UserID, b.Kind, b.Path, b.SHA256, b.Size, ts)
	if err != nil {
		return BlobRecord{}, err
	}
	return s.GetBlobByPathForUser(ctx, b.UserID, b.Path)
}

func (s *Store) GetBlobForUser(ctx context.Context, userID, id int64) (BlobRecord, error) {
	var b BlobRecord
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, kind, path, sha256, size, created_at FROM blobs WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&b.ID, &b.UserID, &b.Kind, &b.Path, &b.SHA256, &b.Size, &created)
	b.CreatedAt = unixTime(created)
	return b, err
}

func (s *Store) GetBlobByPathForUser(ctx context.Context, userID int64, blobPath string) (BlobRecord, error) {
	var b BlobRecord
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, kind, path, sha256, size, created_at FROM blobs WHERE user_id = ? AND path = ?`, userID, blobPath).
		Scan(&b.ID, &b.UserID, &b.Kind, &b.Path, &b.SHA256, &b.Size, &created)
	b.CreatedAt = unixTime(created)
	return b, err
}

func (s *Store) DeleteBlobForUser(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM blobs WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	ts := nowUnix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO attachments (user_id, message_id, blob_id, filename, content_type, content_id, is_inline, size, blob_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.UserID, a.MessageID, a.BlobID, a.Filename, a.ContentType, a.ContentID, boolInt(a.IsInline), a.Size, a.BlobPath, ts)
	if err != nil {
		return Attachment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Attachment{}, err
	}
	return s.GetAttachmentForUser(ctx, a.UserID, id)
}

func (s *Store) DeleteAttachmentsForMessage(ctx context.Context, userID, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM attachments WHERE user_id = ? AND message_id = ?`, userID, messageID)
	return err
}

func (s *Store) GetAttachmentForUser(ctx context.Context, userID, id int64) (Attachment, error) {
	var a Attachment
	var created int64
	var isInline int
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, message_id, blob_id, filename, content_type, content_id, is_inline, size, blob_path, created_at
		FROM attachments WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&a.ID, &a.UserID, &a.MessageID, &a.BlobID, &a.Filename, &a.ContentType, &a.ContentID, &isInline, &a.Size, &a.BlobPath, &created)
	a.IsInline = isInline != 0
	a.CreatedAt = unixTime(created)
	return a, err
}

func (s *Store) ListAttachmentsForMessage(ctx context.Context, userID, messageID int64) ([]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, message_id, blob_id, filename, content_type, content_id, is_inline, size, blob_path, created_at
		FROM attachments WHERE user_id = ? AND message_id = ? ORDER BY id`, userID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		var a Attachment
		var created int64
		var isInline int
		if err := rows.Scan(&a.ID, &a.UserID, &a.MessageID, &a.BlobID, &a.Filename, &a.ContentType, &a.ContentID, &isInline, &a.Size, &a.BlobPath, &created); err != nil {
			return nil, err
		}
		a.IsInline = isInline != 0
		a.CreatedAt = unixTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateSyncRun(ctx context.Context, userID, accountID int64) (SyncRun, error) {
	started := nowUnix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO sync_runs (user_id, account_id, status, started_at, updated_at) VALUES (?, ?, 'running', ?, ?)`, userID, accountID, started, started)
	if err != nil {
		return SyncRun{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SyncRun{}, err
	}
	return s.GetSyncRunForUser(ctx, userID, id)
}

func (s *Store) MarkRunningSyncRunsInterrupted(ctx context.Context) (int64, error) {
	now := nowUnix()
	res, err := s.db.ExecContext(ctx, `UPDATE sync_runs
		SET status = 'interrupted', finished_at = ?, updated_at = ?, error = CASE WHEN error = '' THEN 'Server restarted before this sync finished.' ELSE error END
		WHERE status = 'running'`, now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type SyncProgress struct {
	MessagesSeen    int
	MessagesStored  int
	MessagesSkipped int
	NewMessages     int
	MessagesTotal   int
	MailboxesDone   int
	MailboxesTotal  int
	CurrentMailbox  string
	CurrentUID      uint32
}

func (s *Store) UpdateSyncRunProgress(ctx context.Context, userID, id int64, p SyncProgress) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_runs
		SET updated_at = ?, messages_seen = ?, messages_stored = ?, messages_skipped = ?, new_messages = ?, messages_total = ?, mailboxes_done = ?, mailboxes_total = ?, current_mailbox = ?, current_uid = ?
		WHERE user_id = ? AND id = ?`,
		nowUnix(), p.MessagesSeen, p.MessagesStored, p.MessagesSkipped, p.NewMessages, p.MessagesTotal, p.MailboxesDone, p.MailboxesTotal, p.CurrentMailbox, p.CurrentUID, userID, id)
	return err
}

func (s *Store) FinishSyncRun(ctx context.Context, userID, id int64, status string, p SyncProgress, errText string) error {
	if len(errText) > 1000 {
		errText = errText[:1000]
	}
	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `UPDATE sync_runs
		SET status = ?, finished_at = ?, updated_at = ?, messages_seen = ?, messages_stored = ?, messages_skipped = ?, new_messages = ?, messages_total = ?,
			mailboxes_done = ?, mailboxes_total = ?, current_mailbox = ?, current_uid = ?, error = ?
		WHERE user_id = ? AND id = ? AND NOT (status = 'interrupted' AND finished_at != 0)`,
		status, now, now, p.MessagesSeen, p.MessagesStored, p.MessagesSkipped, p.NewMessages, p.MessagesTotal, p.MailboxesDone, p.MailboxesTotal,
		p.CurrentMailbox, p.CurrentUID, errText, userID, id)
	return err
}

func (s *Store) GetSyncRunForUser(ctx context.Context, userID, id int64) (SyncRun, error) {
	var r SyncRun
	var started, finished, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, account_id, status, started_at, finished_at, updated_at,
			messages_seen, messages_stored, messages_skipped, new_messages, messages_total, mailboxes_done, mailboxes_total, current_mailbox, current_uid, error
		FROM sync_runs WHERE user_id = ? AND id = ?`, userID, id).
		Scan(&r.ID, &r.UserID, &r.AccountID, &r.Status, &started, &finished, &updated,
			&r.MessagesSeen, &r.MessagesStored, &r.MessagesSkipped, &r.NewMessages, &r.MessagesTotal, &r.MailboxesDone, &r.MailboxesTotal, &r.CurrentMailbox, &r.CurrentUID, &r.Error)
	r.StartedAt = unixTime(started)
	r.FinishedAt = unixTime(finished)
	r.UpdatedAt = unixTime(updated)
	return r, err
}

func (s *Store) ListSyncRunsForUser(ctx context.Context, userID int64, limit int) ([]SyncRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, status, started_at, finished_at, updated_at,
			messages_seen, messages_stored, messages_skipped, new_messages, messages_total, mailboxes_done, mailboxes_total, current_mailbox, current_uid, error
		FROM sync_runs WHERE user_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncRun
	for rows.Next() {
		var r SyncRun
		var started, finished, updated int64
		if err := rows.Scan(&r.ID, &r.UserID, &r.AccountID, &r.Status, &started, &finished, &updated,
			&r.MessagesSeen, &r.MessagesStored, &r.MessagesSkipped, &r.NewMessages, &r.MessagesTotal, &r.MailboxesDone, &r.MailboxesTotal, &r.CurrentMailbox, &r.CurrentUID, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt = unixTime(started)
		r.FinishedAt = unixTime(finished)
		r.UpdatedAt = unixTime(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListUserIDsWithAccounts(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM mail_accounts ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func WrapNotFound(thing string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", thing, ErrNotFound)
	}
	return err
}
