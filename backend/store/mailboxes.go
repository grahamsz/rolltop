// File overview: Mail account and mailbox persistence, defaults, hierarchy, and sync-mode helpers.

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const DefaultMailboxPattern = "*"

func prepareMailAccount(a MailAccount) (MailAccount, error) {
	if a.UserID == 0 || strings.TrimSpace(a.Email) == "" || strings.TrimSpace(a.Host) == "" || strings.TrimSpace(a.Username) == "" || a.Port == 0 || a.EncryptedPassword == "" {
		return MailAccount{}, errors.New("mail account fields are incomplete")
	}
	if strings.TrimSpace(a.Mailbox) == "" {
		a.Mailbox = DefaultMailboxPattern
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
	a.Email = strings.TrimSpace(a.Email)
	a.Label = trimLimit(strings.TrimSpace(a.Label), 240)
	if a.Label == "" {
		a.Label = firstNonEmpty(a.Email, a.Username, a.Host)
	}
	a.Host = strings.TrimSpace(a.Host)
	a.Username = strings.TrimSpace(a.Username)
	a.SMTPHost = strings.TrimSpace(a.SMTPHost)
	a.SMTPUsername = strings.TrimSpace(a.SMTPUsername)
	a.Mailbox = strings.TrimSpace(a.Mailbox)
	return a, nil
}

func (s *Store) CreateMailAccount(ctx context.Context, a MailAccount) (MailAccount, error) {
	a, err := prepareMailAccount(a)
	if err != nil {
		return MailAccount{}, err
	}
	ts := nowUnix()
	res, err := s.mustDataDB(ctx, a.UserID).ExecContext(ctx, `INSERT INTO mail_accounts
			(user_id, email, label, host, port, username, encrypted_password, use_tls, smtp_host, smtp_port, smtp_username, encrypted_smtp_password, smtp_use_tls, mailbox, sync_interval_minutes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, a.Email, a.Label, a.Host, a.Port, a.Username, a.EncryptedPassword,
		boolInt(a.UseTLS), a.SMTPHost, a.SMTPPort, a.SMTPUsername, a.EncryptedSMTPPassword,
		boolInt(a.SMTPUseTLS), a.Mailbox, a.SyncIntervalMinutes, ts, ts)
	if err != nil {
		return MailAccount{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MailAccount{}, err
	}
	return s.GetMailAccountForUser(ctx, a.UserID, id)
}

func (s *Store) UpsertMailAccount(ctx context.Context, a MailAccount) (MailAccount, error) {
	if a.ID == 0 {
		if existing, err := s.GetMailAccount(ctx, a.UserID); err == nil {
			a.ID = existing.ID
		} else if !IsNotFound(err) {
			return MailAccount{}, err
		}
	}
	if a.ID == 0 {
		return s.CreateMailAccount(ctx, a)
	}
	return s.updateMailAccount(ctx, a)
}

func (s *Store) updateMailAccount(ctx context.Context, a MailAccount) (MailAccount, error) {
	a, err := prepareMailAccount(a)
	if err != nil {
		return MailAccount{}, err
	}
	res, err := s.mustDataDB(ctx, a.UserID).ExecContext(ctx, `UPDATE mail_accounts SET
			email = ?, label = ?, host = ?, port = ?, username = ?, encrypted_password = ?, use_tls = ?, smtp_host = ?, smtp_port = ?, smtp_username = ?, encrypted_smtp_password = ?, smtp_use_tls = ?, mailbox = ?, sync_interval_minutes = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`,
		a.Email, a.Label, a.Host, a.Port, a.Username, a.EncryptedPassword, boolInt(a.UseTLS), a.SMTPHost, a.SMTPPort, a.SMTPUsername, a.EncryptedSMTPPassword,
		boolInt(a.SMTPUseTLS), a.Mailbox, a.SyncIntervalMinutes, nowUnix(), a.UserID, a.ID)
	if err != nil {
		return MailAccount{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return MailAccount{}, err
	}
	if n == 0 {
		return MailAccount{}, ErrNotFound
	}
	return s.GetMailAccountForUser(ctx, a.UserID, a.ID)
}

func (s *Store) GetMailAccount(ctx context.Context, userID int64) (MailAccount, error) {
	return scanMailAccount(s.mustDataDB(ctx, userID).QueryRowContext(ctx, mailAccountSelectSQL()+` WHERE user_id = ? ORDER BY id LIMIT 1`, userID))
}

func (s *Store) GetMailAccountForUser(ctx context.Context, userID, accountID int64) (MailAccount, error) {
	if accountID <= 0 {
		return MailAccount{}, ErrNotFound
	}
	return scanMailAccount(s.mustDataDB(ctx, userID).QueryRowContext(ctx, mailAccountSelectSQL()+` WHERE user_id = ? AND id = ?`, userID, accountID))
}

func (s *Store) ListMailAccountsForUser(ctx context.Context, userID int64) ([]MailAccount, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, mailAccountSelectSQL()+` WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMailAccounts(rows)
}

func (s *Store) ListAccounts(ctx context.Context) ([]MailAccount, error) {
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		out := []MailAccount{}
		for _, user := range users {
			accounts, err := s.ListMailAccountsForUser(ctx, user.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, accounts...)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, mailAccountSelectSQL()+` ORDER BY user_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMailAccounts(rows)
}

func mailAccountSelectSQL() string {
	return `SELECT id, user_id, email, label, host, port, username, encrypted_password, use_tls, smtp_host, smtp_port, smtp_username, encrypted_smtp_password, smtp_use_tls, mailbox, sync_interval_minutes, created_at, updated_at FROM mail_accounts`
}

func scanMailAccounts(rows *sql.Rows) ([]MailAccount, error) {
	var accounts []MailAccount
	for rows.Next() {
		a, err := scanMailAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func scanMailAccount(row rowScanner) (MailAccount, error) {
	var a MailAccount
	var created, updated int64
	err := row.Scan(&a.ID, &a.UserID, &a.Email, &a.Label, &a.Host, &a.Port, &a.Username, &a.EncryptedPassword, &a.UseTLS, &a.SMTPHost, &a.SMTPPort, &a.SMTPUsername, &a.EncryptedSMTPPassword, &a.SMTPUseTLS, &a.Mailbox, &a.SyncIntervalMinutes, &created, &updated)
	a.CreatedAt = unixTime(created)
	a.UpdatedAt = unixTime(updated)
	a.applySMTPDefaults()
	return a, err
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
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `INSERT INTO mailboxes (user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, 1, ?, ?)
		ON CONFLICT(user_id, account_id, name) DO NOTHING`, userID, accountID, name, syncMode, role, icon, boolInt(showInAllMail), ts, ts)
	if err != nil {
		return Mailbox{}, err
	}
	return s.GetMailbox(ctx, userID, accountID, name)
}

func (s *Store) NextUIDForMailbox(ctx context.Context, userID, mailboxID int64) (uint32, error) {
	var next uint32
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT COALESCE(MAX(uid), 0) + 1 FROM messages WHERE user_id = ? AND mailbox_id = ?`, userID, mailboxID).Scan(&next)
	if next == 0 {
		next = 1
	}
	return next, err
}

func (s *Store) GetMailbox(ctx context.Context, userID, accountID int64, name string) (Mailbox, error) {
	var m Mailbox
	var created, updated, statusChecked int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, uidvalidity, last_uid,
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
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, uidvalidity, last_uid,
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
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT mb.id, mb.user_id, mb.account_id, mb.name, mb.sync_mode, mb.role, mb.icon,
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
		ms.LocalMessageCount = localMessages
		ms.MessageCount = localMessages
		ms.UnreadCount = localUnread
		if statusChecked > 0 {
			ms.MessageCount = ms.RemoteMessageCount
			ms.UnreadCount = ms.RemoteUnreadCount
		}
		ms.SyncPercent = mailboxSyncPercent(ms.LastUID, ms.RemoteUIDNext, ms.MessageCount)
		ms.LocalSyncPercent = ms.SyncPercent
		out = append(out, ms)
	}
	return out, rows.Err()
}

func (s *Store) LastUIDs(ctx context.Context, userID, accountID int64) (map[string]uint32, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT name, last_uid FROM mailboxes WHERE user_id = ? AND account_id = ?`, userID, accountID)
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
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE mailboxes SET sync_mode = ?, updated_at = ? WHERE user_id = ? AND id = ?`, mode, nowUnix(), userID, mailboxID)
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
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if settings.Role == "inbox" || settings.Role == "sent" || settings.Role == "drafts" || settings.Role == "trash" {
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
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
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
	case "sent":
		return "sent"
	case "draft", "drafts":
		return "drafts"
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
	if strings.EqualFold(strings.TrimSpace(name), "INBOX") {
		return "auto"
	}
	return "manual"
}

func defaultMailboxRole(name string) string {
	clean := strings.ToLower(strings.TrimSpace(name))
	switch clean {
	case "inbox":
		return "inbox"
	case "sent", "sent mail", "sent items", "[gmail]/sent mail":
		return "sent"
	case "draft", "drafts", "[gmail]/drafts":
		return "drafts"
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
	case "sent":
		return "send"
	case "drafts":
		return "draft"
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
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE mailboxes SET last_uid = CASE WHEN last_uid < ? THEN ? ELSE last_uid END, updated_at = ?
		WHERE id = ? AND user_id = ?`, uid, uid, nowUnix(), mailboxID, userID)
	return err
}

func (s *Store) UpdateMailboxRemoteStatus(ctx context.Context, userID, mailboxID int64, messageCount, unreadCount int, uidNext uint32, uidValidity uint32) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE mailboxes
		SET remote_message_count = ?, remote_unread_count = ?, remote_uid_next = ?, uidvalidity = ?, status_checked_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		messageCount, unreadCount, uidNext, uidValidity, nowUnix(), nowUnix(), mailboxID, userID)
	return err
}

func (s *Store) MessageExistsByUID(ctx context.Context, userID, accountID, mailboxID int64, uid uint32) (bool, error) {
	var id int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id FROM messages WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND uid = ?`,
		userID, accountID, mailboxID, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
