package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) ListMessagesForUser(ctx context.Context, userID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? ORDER BY date_unix DESC, id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) ListMessagesForMailbox(ctx context.Context, userID, mailboxID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND mailbox_id = ? ORDER BY date_unix DESC, id DESC LIMIT ? OFFSET ?`, userID, mailboxID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (s *Store) CountMessagesForMailbox(ctx context.Context, userID, mailboxID int64) (int, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ? AND mailbox_id = ?`, userID, mailboxID).Scan(&n)
	return n, err
}

func (s *Store) ListLatestThreadMessagesForUser(ctx context.Context, userID int64, limit, offset int) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `WITH keyed AS (
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
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `WITH keyed AS (
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
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND thread_key = ? ORDER BY date_unix ASC, id ASC`, userID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

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
