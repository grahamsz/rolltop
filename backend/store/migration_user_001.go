// File overview: User database migration 001. Store.userStore applies this
// migration to data/users/<id>/mailmirror.db when a user is prepared at startup
// or touched lazily at runtime. The syncer writes IMAP accounts, mailboxes,
// messages, locations, attachments, blob metadata, and sync progress here. The
// web/API layer reads these same tables for mailbox lists, message views,
// search result hydration, contacts, identities, compose defaults, and user
// plugin state. This keeps all mailbox-owner data removable by deleting one
// user directory.

package store

import "context"

// userMigrationSet returns the clean-start tenant schema. The system database
// mirrors only the user row into each user DB; everything else in this file is
// scoped by user_id and belongs to that mailbox owner.
func userMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion,
		Label:   "user schema",
		Statements: []string{
			// The mirrored user row lets foreign keys work inside the user DB without
			// letting the user DB become the source of truth for authentication.
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
			// Account and mailbox configuration is consumed by syncer and by the
			// settings UI. Credentials are already encrypted before reaching SQLite.
			`CREATE TABLE IF NOT EXISTS mail_accounts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				email TEXT NOT NULL,
				label TEXT NOT NULL DEFAULT '',
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
			// Messages are the canonical indexed metadata rows. Raw MIME bodies stay in
			// the user blob directory and are linked through blobs/blob_path.
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
			// Contacts and identities back compose defaults, sender matching, and the
			// settings UI for per-user outgoing identity configuration.
			`CREATE TABLE IF NOT EXISTS contacts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name_prefix TEXT NOT NULL DEFAULT '',
				given_name TEXT NOT NULL DEFAULT '',
				additional_name TEXT NOT NULL DEFAULT '',
				family_name TEXT NOT NULL DEFAULT '',
				name_suffix TEXT NOT NULL DEFAULT '',
				display_name TEXT NOT NULL DEFAULT '',
				nickname TEXT NOT NULL DEFAULT '',
				organization TEXT NOT NULL DEFAULT '',
				department TEXT NOT NULL DEFAULT '',
				job_title TEXT NOT NULL DEFAULT '',
				birthday TEXT NOT NULL DEFAULT '',
				notes TEXT NOT NULL DEFAULT '',
				categories TEXT NOT NULL DEFAULT '',
				is_me INTEGER NOT NULL DEFAULT 0,
				is_primary INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS contact_emails (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				label TEXT NOT NULL DEFAULT '',
				email TEXT NOT NULL,
				normalized_email TEXT NOT NULL,
				is_primary INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS contact_phones (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				label TEXT NOT NULL DEFAULT '',
				number TEXT NOT NULL,
				is_primary INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS contact_addresses (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				label TEXT NOT NULL DEFAULT '',
				street TEXT NOT NULL DEFAULT '',
				locality TEXT NOT NULL DEFAULT '',
				region TEXT NOT NULL DEFAULT '',
				postal_code TEXT NOT NULL DEFAULT '',
				country TEXT NOT NULL DEFAULT '',
				is_primary INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS contact_urls (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				label TEXT NOT NULL DEFAULT '',
				url TEXT NOT NULL,
				is_primary INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS contact_icons (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				blob_id INTEGER NOT NULL REFERENCES blobs(id) ON DELETE RESTRICT,
				content_type TEXT NOT NULL,
				filename TEXT NOT NULL DEFAULT '',
				size INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, contact_id)
			)`,
			`CREATE TABLE IF NOT EXISTS smtp_accounts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				label TEXT NOT NULL DEFAULT '',
				host TEXT NOT NULL,
				port INTEGER NOT NULL DEFAULT 587,
				username TEXT NOT NULL DEFAULT '',
				encrypted_password TEXT NOT NULL DEFAULT '',
				use_tls INTEGER NOT NULL DEFAULT 1,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS mail_identities (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
				contact_email_id INTEGER NOT NULL REFERENCES contact_emails(id) ON DELETE CASCADE,
				smtp_account_id INTEGER NOT NULL DEFAULT 0,
				email TEXT NOT NULL,
				display_name TEXT NOT NULL DEFAULT '',
				signature TEXT NOT NULL DEFAULT '',
				is_primary INTEGER NOT NULL DEFAULT 0,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL,
				UNIQUE(user_id, contact_email_id)
			)`,
			// Sync runs are operational state for the runner and the frontend sync widget.
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
				latest_new_from TEXT NOT NULL DEFAULT '',
				latest_new_subject TEXT NOT NULL DEFAULT '',
				messages_total INTEGER NOT NULL DEFAULT 0,
				mailboxes_done INTEGER NOT NULL DEFAULT 0,
				mailboxes_total INTEGER NOT NULL DEFAULT 0,
				current_mailbox TEXT NOT NULL DEFAULT '',
				current_uid INTEGER NOT NULL DEFAULT 0,
				error TEXT NOT NULL DEFAULT ''
			)`,
			// Per-user plugin tables store cached lookup results or user choices that
			// should disappear with the owning account.
			`CREATE TABLE IF NOT EXISTS plugin_bimi_brand_icons (
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
			`CREATE TABLE IF NOT EXISTS plugin_gravatar_cache (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				email_hash TEXT NOT NULL,
				content_type TEXT NOT NULL DEFAULT '',
				image BLOB,
				status TEXT NOT NULL DEFAULT '',
				error TEXT NOT NULL DEFAULT '',
				fetched_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL DEFAULT 0,
				UNIQUE(user_id, email_hash)
			)`,
			`CREATE TABLE IF NOT EXISTS plugin_language_messages (
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				language_code TEXT NOT NULL,
				detected_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, message_id)
			)`,
			`CREATE TABLE IF NOT EXISTS plugin_one_click_unsubscribe_sends (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				sender TEXT NOT NULL DEFAULT '',
				unsubscribe_url TEXT NOT NULL,
				sent_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				UNIQUE(user_id, message_id, unsubscribe_url)
			)`,
			`CREATE TABLE IF NOT EXISTS plugin_trusted_image_sources (
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				sender TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, sender)
			)`,
			// These indexes cover the hot mailbox list, thread view, search hydration,
			// contact lookup, sync history, and plugin-cache access paths.
			`CREATE INDEX IF NOT EXISTS idx_mail_accounts_user ON mail_accounts(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_smtp_accounts_user ON smtp_accounts(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_mail_identities_user ON mail_identities(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_mail_identities_smtp ON mail_identities(user_id, smtp_account_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_date ON messages(user_id, date_unix DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_mailbox_read ON messages(user_id, mailbox_id, is_read)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_thread ON messages(user_id, thread_key, date_unix, id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_mailbox_thread ON messages(user_id, mailbox_id, thread_key, date_unix, id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_user_starred ON messages(user_id, is_starred, date_unix DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_thread_headers_checked ON messages(thread_headers_checked_at, id)`,
			`CREATE INDEX IF NOT EXISTS idx_attachments_user_message ON attachments(user_id, message_id)`,
			`CREATE INDEX IF NOT EXISTS idx_blobs_user ON blobs(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_contacts_user_name ON contacts(user_id, display_name COLLATE NOCASE)`,
			`CREATE INDEX IF NOT EXISTS idx_contacts_user_me ON contacts(user_id, is_me, is_primary)`,
			`CREATE INDEX IF NOT EXISTS idx_contact_emails_user_contact ON contact_emails(user_id, contact_id)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_contact_emails_user_normalized ON contact_emails(user_id, normalized_email) WHERE normalized_email <> ''`,
			`CREATE INDEX IF NOT EXISTS idx_contact_phones_user_contact ON contact_phones(user_id, contact_id)`,
			`CREATE INDEX IF NOT EXISTS idx_contact_addresses_user_contact ON contact_addresses(user_id, contact_id)`,
			`CREATE INDEX IF NOT EXISTS idx_contact_urls_user_contact ON contact_urls(user_id, contact_id)`,
			`CREATE INDEX IF NOT EXISTS idx_contact_icons_user_contact ON contact_icons(user_id, contact_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sync_runs_user ON sync_runs(user_id, started_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_bimi_brand_icons_user_domain ON plugin_bimi_brand_icons(user_id, domain)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_gravatar_cache_user_hash ON plugin_gravatar_cache(user_id, email_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_language_messages_user_language ON plugin_language_messages(user_id, language_code)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_one_click_unsubscribe_user_message ON plugin_one_click_unsubscribe_sends(user_id, message_id, sent_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_one_click_unsubscribe_user_url ON plugin_one_click_unsubscribe_sends(user_id, unsubscribe_url, sent_at DESC)`,
		},
		After: []migrationStep{
			{Label: "seed smtp accounts", Run: func(ctx context.Context, s *Store) error { return s.seedSMTPAccountsFromMailAccounts(ctx) }},
			{Label: "normalize mailbox roles", Run: seedMailboxRoleDefaults},
			{Label: "backfill language index", Run: seedLanguageMessageRows},
		},
	}
}

func seedMailboxRoleDefaults(ctx context.Context, s *Store) error {
	now := nowUnix()
	updates := []string{
		`UPDATE mailboxes SET role = 'inbox', icon = 'inbox', updated_at = ? WHERE role = '' AND lower(name) = 'inbox'`,
		`UPDATE mailboxes SET role = 'sent', icon = 'send', updated_at = ? WHERE role = '' AND lower(name) IN ('sent', 'sent mail', 'sent items', '[gmail]/sent mail')`,
		`UPDATE mailboxes SET role = 'drafts', icon = 'draft', updated_at = ? WHERE role = '' AND lower(name) IN ('draft', 'drafts', '[gmail]/drafts')`,
		`UPDATE mailboxes SET role = 'trash', icon = 'delete', show_in_all_mail = 0, updated_at = ? WHERE role = '' AND lower(name) IN ('trash', 'deleted', 'deleted items', '[gmail]/trash')`,
	}
	for _, stmt := range updates {
		if _, err := s.db.ExecContext(ctx, stmt, now); err != nil {
			return err
		}
	}
	return nil
}

func seedLanguageMessageRows(ctx context.Context, s *Store) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO plugin_language_messages
			(user_id, message_id, language_code, detected_at)
		SELECT user_id, id, lower(trim(language_code)), updated_at
		FROM messages
		WHERE trim(language_code) <> ''`)
	return err
}
