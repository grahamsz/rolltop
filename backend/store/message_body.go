// File overview: Message body retrieval and cached blob fallback helpers.

package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// MessageBodyPreview returns a bounded UTF-8-safe prefix for body previews and compound search fields.
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

// UpdateMessageBodies stores display-ready bodies discovered from a cached raw
// message while preserving the user/message ownership boundary.
func (s *Store) UpdateMessageBodies(ctx context.Context, userID, messageID int64, bodyText, bodyHTML string) error {
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET body_text = ?, body_html = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, bodyText, bodyHTML, nowUnix(), userID, messageID)
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

// GetMessageBodiesForUser loads stored display bodies for one user-owned message.
func (s *Store) GetMessageBodiesForUser(ctx context.Context, userID, messageID int64) (string, string, error) {
	var bodyText, bodyHTML string
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT body_text, body_html FROM messages WHERE user_id = ? AND id = ?`, userID, messageID).Scan(&bodyText, &bodyHTML)
	return bodyText, bodyHTML, err
}

// CompactMessageBodiesBefore replaces old full bodies with previews after raw blobs age out.
func (s *Store) CompactMessageBodiesBefore(ctx context.Context, cutoff time.Time, previewLimit, limit int) (int, error) {
	if previewLimit <= 0 {
		previewLimit = DefaultMessageBodyPreviewBytes
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return 0, err
		}
		total := 0
		remaining := limit
		for _, user := range users {
			if remaining <= 0 {
				break
			}
			us, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return total, err
			}
			n, err := us.CompactMessageBodiesBefore(ctx, cutoff, previewLimit, remaining)
			if err != nil {
				return total, err
			}
			total += n
			remaining -= n
		}
		return total, nil
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

// ListMessagesWithPrunableBlobs finds old message blobs eligible for retention pruning.
func (s *Store) ListMessagesWithPrunableBlobs(ctx context.Context, cutoff time.Time, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]MessageRecord, 0, limit)
		for _, user := range users {
			if len(out) >= limit {
				break
			}
			us, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return nil, err
			}
			items, err := us.ListMessagesWithPrunableBlobs(ctx, cutoff, limit-len(out))
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE blob_path != '' AND date_unix < ? ORDER BY date_unix, id LIMIT ?`, cutoff.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessagesWithExpiredCachedBlobs finds temporary on-demand blobs past their cache window.
func (s *Store) ListMessagesWithExpiredCachedBlobs(ctx context.Context, cutoff time.Time, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if s.split {
		users, err := s.ListUsers(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]MessageRecord, 0, limit)
		for _, user := range users {
			if len(out) >= limit {
				break
			}
			us, err := s.UserStore(ctx, user.ID)
			if err != nil {
				return nil, err
			}
			items, err := us.ListMessagesWithExpiredCachedBlobs(ctx, cutoff, limit-len(out))
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.user_id, m.account_id, m.mailbox_id, m.blob_id, m.message_id_header, m.in_reply_to, m.references_header, m.thread_key, m.subject, m.language_code, m.from_addr, m.to_addr, m.cc_addr,
			m.date_unix, m.internal_date_unix, m.uid, m.size, m.blob_path, m.body_text, m.body_html, m.is_read, m.read_sync_pending, m.is_starred, m.star_sync_pending, m.has_attachments, m.is_encrypted, m.is_signed, m.attachment_indexed_at, m.created_at, m.updated_at
		FROM messages m
		JOIN blobs b ON b.user_id = m.user_id AND b.id = m.blob_id
		WHERE m.blob_path != '' AND b.kind = 'message-cache' AND b.created_at < ?
		ORDER BY b.created_at, m.id LIMIT ?`, cutoff.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// CacheMessageBlob attaches a freshly fetched temporary raw blob to an existing message row.
func (s *Store) CacheMessageBlob(ctx context.Context, userID, messageID int64, blob BlobRecord) (MessageRecord, error) {
	blob.UserID = userID
	blob.Kind = "message-cache"
	blobRec, err := s.CreateBlob(ctx, blob)
	if err != nil {
		return MessageRecord{}, err
	}
	res, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET blob_id = ?, blob_path = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
		blobRec.ID, blobRec.Path, nowUnix(), userID, messageID)
	if err != nil {
		return MessageRecord{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return MessageRecord{}, err
	}
	if n == 0 {
		return MessageRecord{}, ErrNotFound
	}
	return s.GetMessageForUser(ctx, userID, messageID)
}

// MarkMessageBlobPruned clears a message's blob link after the corresponding raw file is removed.
func (s *Store) MarkMessageBlobPruned(ctx context.Context, userID, messageID, blobID int64) error {
	tx, err := s.mustDataDB(ctx, userID).BeginTx(ctx, nil)
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
