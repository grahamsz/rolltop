// File overview: Mailbox, all-mail, search seed, sender stat, and date-window query helpers.

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ListMessagesForUser returns recent messages across visible mailboxes for one user.
func (s *Store) ListMessagesForUser(ctx context.Context, userID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.is_encrypted, m.is_signed, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM messages m
		LEFT JOIN message_snoozes sn ON sn.user_id = m.user_id
			AND sn.thread_key = COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id)
		WHERE m.user_id = ? AND (sn.id IS NULL OR sn.snoozed_until <= ?)
		ORDER BY CASE WHEN COALESCE(sn.snoozed_until, 0) > m.date_unix THEN sn.snoozed_until ELSE m.date_unix END DESC, m.id DESC
		LIMIT ? OFFSET ?`, userID, nowUnix(), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessagesForMailbox returns recent messages from one user-owned mailbox.
func (s *Store) ListMessagesForMailbox(ctx context.Context, userID, mailboxID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.is_encrypted, m.is_signed, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM messages m
		LEFT JOIN message_snoozes sn ON sn.user_id = m.user_id
			AND sn.thread_key = COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id)
		WHERE m.user_id = ? AND m.mailbox_id = ? AND (sn.id IS NULL OR sn.snoozed_until <= ?)
		ORDER BY CASE WHEN COALESCE(sn.snoozed_until, 0) > m.date_unix THEN sn.snoozed_until ELSE m.date_unix END DESC, m.id DESC
		LIMIT ? OFFSET ?`, userID, mailboxID, nowUnix(), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// CountMessagesForUser counts all local message header rows for one user.
// Storage reporting presents this as Message Headers because SQLite keeps the
// message envelope, flags, thread fields, and preview text even when the raw body
// has been pruned or never cached locally.
func (s *Store) CountMessagesForUser(ctx context.Context, userID int64) (int, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// CountCachedMessageBodiesForUser counts messages whose raw RFC822 body is
// currently held in the local blob store. Remote-only placeholder blobs are not
// counted because there is no local body file behind them.
func (s *Store) CountCachedMessageBodiesForUser(ctx context.Context, userID int64) (int, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM messages m
		JOIN blobs b ON b.user_id = m.user_id AND b.id = m.blob_id
		WHERE m.user_id = ? AND m.blob_path != '' AND b.kind IN ('message', 'message-cache') AND b.size > 0`, userID).Scan(&n)
	return n, err
}

// CountMessagesForMailbox counts local mirrored messages in one user-owned mailbox.
func (s *Store) CountMessagesForMailbox(ctx context.Context, userID, mailboxID int64) (int, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ? AND mailbox_id = ?`, userID, mailboxID).Scan(&n)
	return n, err
}

// CountSearchEnabledMessagesForUser counts local messages in folders included in
// full-text search. Settings storage uses this alongside Bleve's document count
// to show whether SQLite mail and the search index are in the same ballpark.
func (s *Store) CountSearchEnabledMessagesForUser(ctx context.Context, userID int64) (int, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM messages m
		JOIN mailboxes mb ON mb.id = m.mailbox_id AND mb.user_id = m.user_id
		WHERE m.user_id = ? AND mb.include_in_search = 1`, userID).Scan(&n)
	return n, err
}

// ListRecentSearchEnabledMessagesForUser returns the newest local messages from
// folders that participate in full-text search. The web layer uses this as a
// bounded self-healing pass before search requests so a failed/interrupted Bleve
// commit does not leave today's mail undiscoverable until a manual repair.
func (s *Store) ListRecentSearchEnabledMessagesForUser(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.is_encrypted, m.is_signed, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM messages m
		JOIN mailboxes mb ON mb.id = m.mailbox_id AND mb.user_id = m.user_id
		WHERE m.user_id = ? AND mb.include_in_search = 1
		ORDER BY m.date_unix DESC, m.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListLatestThreadMessagesForUser returns one latest message per thread for all-mail list rendering.
func (s *Store) ListLatestThreadMessagesForUser(ctx context.Context, userID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `WITH keyed AS (
			SELECT COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id) AS thread_group,
				MAX(printf('%020d:%020d',
					CASE WHEN COALESCE(sn.snoozed_until, 0) > m.date_unix THEN sn.snoozed_until ELSE m.date_unix END,
					m.id)) AS latest_key
			FROM messages m
			JOIN mailboxes mb ON mb.id = m.mailbox_id AND mb.user_id = m.user_id
			LEFT JOIN message_snoozes sn ON sn.user_id = m.user_id
				AND sn.thread_key = COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id)
			WHERE m.user_id = ? AND mb.show_in_all_mail = 1 AND (sn.id IS NULL OR sn.snoozed_until <= ?)
			GROUP BY thread_group
			ORDER BY latest_key DESC LIMIT ? OFFSET ?
		)
		SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.is_encrypted, m.is_signed, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM keyed k JOIN messages m ON m.id = CAST(substr(k.latest_key, 22) AS INTEGER)
		ORDER BY k.latest_key DESC`, userID, nowUnix(), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListLatestThreadMessagesForMailbox returns one latest message per thread within a mailbox.
func (s *Store) ListLatestThreadMessagesForMailbox(ctx context.Context, userID, mailboxID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `WITH keyed AS (
			SELECT COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id) AS thread_group,
				MAX(printf('%020d:%020d',
					CASE WHEN COALESCE(sn.snoozed_until, 0) > m.date_unix THEN sn.snoozed_until ELSE m.date_unix END,
					m.id)) AS latest_key
			FROM messages m
			LEFT JOIN message_snoozes sn ON sn.user_id = m.user_id
				AND sn.thread_key = COALESCE(NULLIF(m.thread_key, ''), 'id:' || m.id)
			WHERE m.user_id = ? AND m.mailbox_id = ? AND (sn.id IS NULL OR sn.snoozed_until <= ?)
			GROUP BY thread_group
			ORDER BY latest_key DESC LIMIT ? OFFSET ?
		)
		SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.is_encrypted, m.is_signed, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM keyed k JOIN messages m ON m.id = CAST(substr(k.latest_key, 22) AS INTEGER)
		ORDER BY k.latest_key DESC`, userID, mailboxID, nowUnix(), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessagesByIDsForUser bulk-loads messages by ID while preserving user ownership checks.
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

// ListThreadMessagesForUser loads all messages in the selected message's conversation.
func (s *Store) ListThreadMessagesForUser(ctx context.Context, userID int64, msg MessageRecord) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(msg.ThreadKey)
	if key == "" {
		key = ThreadKey(msg.MessageIDHeader, msg.InReplyTo, msg.ReferencesHeader, msg.Subject)
	}
	if key == "" {
		return []MessageRecord{msg}, nil
	}
	ids, err := s.threadMessageIDProbe(ctx, db, userID, key)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []MessageRecord{msg}, nil
	}
	if len(ids) == 1 && ids[0] == msg.ID {
		return []MessageRecord{msg}, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND thread_key = ? ORDER BY date_unix ASC, id ASC`, userID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) threadMessageIDProbe(ctx context.Context, db *sql.DB, userID int64, key string) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM messages WHERE user_id = ? AND thread_key = ? ORDER BY date_unix ASC, id ASC LIMIT 2`, userID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListThreadMessagesByKeysForUser groups messages by thread keys for conversation hydration.
func (s *Store) ListThreadMessagesByKeysForUser(ctx context.Context, userID int64, keys []string) (map[string][]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
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
		rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
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
