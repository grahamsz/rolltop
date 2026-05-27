// File overview: Local IMAP-account removal helpers. These methods estimate and
// delete MailMirror-owned SQLite rows in batches without touching remote IMAP mail.

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// AccountPurgeEstimate reports how much local data belongs to one IMAP account.
func (s *Store) AccountPurgeEstimate(ctx context.Context, userID, accountID int64) (AccountPurgeEstimate, error) {
	account, err := s.GetMailAccountForUser(ctx, userID, accountID)
	if err != nil {
		return AccountPurgeEstimate{}, err
	}
	db := s.mustDataDB(ctx, userID)
	estimate := AccountPurgeEstimate{Account: account}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailboxes WHERE user_id = ? AND account_id = ?`, userID, accountID).Scan(&estimate.MailboxCount); err != nil {
		return AccountPurgeEstimate{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ? AND account_id = ?`, userID, accountID).Scan(&estimate.MessageCount); err != nil {
		return AccountPurgeEstimate{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0)
		FROM (
			SELECT b.id, b.size
			FROM messages m
			JOIN blobs b ON b.id = m.blob_id AND b.user_id = m.user_id
			WHERE m.user_id = ? AND m.account_id = ?
			GROUP BY b.id
			UNION
			SELECT b.id, b.size
			FROM messages m
			JOIN attachments a ON a.user_id = m.user_id AND a.message_id = m.id
			JOIN blobs b ON b.id = a.blob_id AND b.user_id = a.user_id
			WHERE m.user_id = ? AND m.account_id = ?
			GROUP BY b.id
		)`, userID, accountID, userID, accountID).Scan(&estimate.BlobCount, &estimate.BlobBytes); err != nil {
		return AccountPurgeEstimate{}, err
	}
	return estimate, nil
}

// ListMailboxesForAccount returns local folders under one user-owned IMAP account.
func (s *Store) ListMailboxesForAccount(ctx context.Context, userID, accountID int64) ([]Mailbox, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, account_id, name, sync_mode, role, icon, show_in_sidebar, show_in_all_mail, include_in_search, uidvalidity, last_uid,
			remote_message_count, remote_unread_count, remote_uid_next, status_checked_at, created_at, updated_at
		FROM mailboxes WHERE user_id = ? AND account_id = ? ORDER BY lower(name)`, userID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mailbox
	for rows.Next() {
		var m Mailbox
		var created, updated, statusChecked int64
		if err := rows.Scan(&m.ID, &m.UserID, &m.AccountID, &m.Name, &m.SyncMode, &m.Role, &m.Icon, &m.ShowInSidebar, &m.ShowInAllMail, &m.IncludeInSearch, &m.UIDValidity, &m.LastUID,
			&m.RemoteMessageCount, &m.RemoteUnreadCount, &m.RemoteUIDNext, &statusChecked, &created, &updated); err != nil {
			return nil, err
		}
		m.SyncMode = normalizeSyncMode(m.SyncMode)
		m.Role = normalizeMailboxRole(m.Role)
		m.Icon = normalizeMailboxIcon(m.Icon, m.Name, m.Role)
		m.StatusCheckedAt = unixTime(statusChecked)
		m.CreatedAt = unixTime(created)
		m.UpdatedAt = unixTime(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}

// PurgeAccountMessageBatch removes a small batch of local message rows for an
// IMAP account and returns the message/attachment blob records those rows used.
func (s *Store) PurgeAccountMessageBatch(ctx context.Context, userID, accountID int64, limit int) ([]BlobRecord, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 250
	}
	db := s.mustDataDB(ctx, userID)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE user_id = ? AND account_id = ? ORDER BY id LIMIT ?`, userID, accountID, limit)
	if err != nil {
		_ = tx.Rollback()
		return nil, 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		_ = tx.Rollback()
		return nil, 0, err
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return nil, 0, err
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, 0, err
		}
		return nil, 0, nil
	}

	refs, err := accountMessageBlobRefs(ctx, tx, userID, ids)
	if err != nil {
		_ = tx.Rollback()
		return nil, 0, err
	}
	deleteArgs := make([]any, 0, len(ids)+1)
	deleteArgs = append(deleteArgs, userID)
	for _, id := range ids {
		deleteArgs = append(deleteArgs, id)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM messages WHERE user_id = ? AND id IN (%s)`, sqlPlaceholders(len(ids))), deleteArgs...); err != nil {
		_ = tx.Rollback()
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return refs, len(ids), nil
}

// ClearIdentityMailboxRefsForAccount drops Sent/Drafts folder pointers that will
// become invalid when the account's mailboxes are deleted.
func (s *Store) ClearIdentityMailboxRefsForAccount(ctx context.Context, userID, accountID int64) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE mail_identities
		SET sent_mailbox_id = CASE WHEN sent_mailbox_id IN (SELECT id FROM mailboxes WHERE user_id = ? AND account_id = ?) THEN 0 ELSE sent_mailbox_id END,
			drafts_mailbox_id = CASE WHEN drafts_mailbox_id IN (SELECT id FROM mailboxes WHERE user_id = ? AND account_id = ?) THEN 0 ELSE drafts_mailbox_id END,
			updated_at = ?
		WHERE user_id = ?`, userID, accountID, userID, accountID, nowUnix(), userID)
	return err
}

// DeleteMailAccountForUser removes the local IMAP server row. Mailboxes,
// messages, and sync runs are removed by SQLite cascades; callers should purge
// message blobs and Bleve documents first if they need progress.
func (s *Store) DeleteMailAccountForUser(ctx context.Context, userID, accountID int64) error {
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM mail_accounts WHERE user_id = ? AND id = ?`, userID, accountID)
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

func accountMessageBlobRefs(ctx context.Context, tx txQueryer, userID int64, messageIDs []int64) ([]BlobRecord, error) {
	placeholders := sqlPlaceholders(len(messageIDs))
	args := make([]any, 0, len(messageIDs)*2+3)
	args = append(args, userID)
	for _, id := range messageIDs {
		args = append(args, id)
	}
	args = append(args, userID)
	for _, id := range messageIDs {
		args = append(args, id)
	}
	args = append(args, userID)
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT b.id, b.user_id, b.kind, b.path, b.sha256, b.size, b.created_at
		FROM blobs b
		JOIN (
			SELECT m.blob_id AS blob_id
			FROM messages m
			WHERE m.user_id = ? AND m.id IN (%s)
			UNION
			SELECT a.blob_id AS blob_id
			FROM attachments a
			JOIN messages m ON m.user_id = a.user_id AND m.id = a.message_id
			WHERE m.user_id = ? AND m.id IN (%s)
		) refs ON refs.blob_id = b.id
		WHERE b.user_id = ?`, placeholders, placeholders), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlobRecord
	for rows.Next() {
		var b BlobRecord
		var created int64
		if err := rows.Scan(&b.ID, &b.UserID, &b.Kind, &b.Path, &b.SHA256, &b.Size, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = unixTime(created)
		out = append(out, b)
	}
	return out, rows.Err()
}

type txQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}
