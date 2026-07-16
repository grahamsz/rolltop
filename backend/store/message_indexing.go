// File overview: Message language and attachment indexing metadata persistence.

package store

import (
	"context"
	"fmt"
	"strings"
)

// MarkSearchVisibleMessagesPendingIndex schedules a complete, tenant-scoped
// search rebuild without changing message content, IMAP state, or blob rows.
func (s *Store) MarkSearchVisibleMessagesPendingIndex(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("user id must be positive")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `UPDATE messages
		SET attachment_indexed_at = 0
		WHERE user_id = ?
			AND mailbox_id IN (
				SELECT id FROM mailboxes WHERE user_id = ? AND include_in_search = 1
			)`, userID, userID)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ListMessagesNeedingAttachmentIndex returns messages whose raw bodies still need attachment text extraction.
func (s *Store) ListMessagesNeedingAttachmentIndex(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	messages, _, err := s.ListMessagesNeedingAttachmentIndexAfter(ctx, userID, 0, limit)
	return messages, err
}

// ListMessagesNeedingAttachmentIndexAfter returns a circular, tenant-scoped
// page after messageID. wrapped reports that the page crossed back to lower IDs.
// The cursor keeps one failed raw message from pinning every later message while
// still bounding each attachment-index turn.
func (s *Store) ListMessagesNeedingAttachmentIndexAfter(ctx context.Context, userID, messageID int64, limit int) ([]MessageRecord, bool, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if messageID < 0 {
		messageID = 0
	}
	messages, err := s.listMessagesNeedingAttachmentIndexRange(ctx, userID, messageID, limit, false)
	if err != nil {
		return nil, false, err
	}
	if messageID == 0 || len(messages) == limit {
		return messages, false, nil
	}
	wrapped, err := s.listMessagesNeedingAttachmentIndexRange(ctx, userID, messageID, limit-len(messages), true)
	if err != nil {
		return nil, false, err
	}
	return append(messages, wrapped...), len(wrapped) > 0, nil
}

func (s *Store) listMessagesNeedingAttachmentIndexRange(ctx context.Context, userID, messageID int64, limit int, throughCursor bool) ([]MessageRecord, error) {
	query := `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND attachment_indexed_at = 0 AND id > ? ORDER BY id LIMIT ?`
	if throughCursor {
		query = `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND attachment_indexed_at = 0 AND id <= ? ORDER BY id LIMIT ?`
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, query, userID, messageID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessagesWithReadSyncPending returns locally changed read-state rows waiting for IMAP sync.
func (s *Store) ListMessagesWithReadSyncPending(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND read_sync_pending = 1 ORDER BY updated_at LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessagesWithStarSyncPending returns locally changed star-state rows waiting for IMAP sync.
func (s *Store) ListMessagesWithStarSyncPending(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, is_encrypted, is_signed, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND star_sync_pending = 1 ORDER BY updated_at LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// MarkMessageAttachmentIndexed records that attachment text extraction ran for a message.
func (s *Store) MarkMessageAttachmentIndexed(ctx context.Context, userID, messageID int64, hasAttachments bool) error {
	result, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET has_attachments = ?, attachment_indexed_at = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(hasAttachments), nowUnix(), nowUnix(), userID, messageID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkMessageAttachmentIndexPending keeps a fallback search document eligible
// for later raw-body and attachment enrichment.
func (s *Store) MarkMessageAttachmentIndexPending(ctx context.Context, userID, messageID int64) error {
	result, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET attachment_indexed_at = 0, updated_at = ?
		WHERE user_id = ? AND id = ?`, nowUnix(), userID, messageID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMessageLanguage stores plugin-detected language metadata for search filtering.
func (s *Store) UpdateMessageLanguage(ctx context.Context, userID, messageID int64, languageCode string) error {
	languageCode = strings.ToLower(strings.TrimSpace(languageCode))
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET language_code = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, strings.ToLower(strings.TrimSpace(languageCode)), nowUnix(), userID, messageID)
	if err != nil {
		return err
	}
	return s.upsertPluginMessageLanguage(ctx, userID, messageID, languageCode)
}

func (s *Store) upsertPluginMessageLanguage(ctx context.Context, userID, messageID int64, languageCode string) error {
	languageCode = strings.ToLower(strings.TrimSpace(languageCode))
	if userID == 0 || messageID == 0 || languageCode == "" {
		return nil
	}
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `INSERT INTO plugin_language_messages
			(user_id, message_id, language_code, detected_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, message_id) DO UPDATE SET
			language_code = excluded.language_code,
			detected_at = excluded.detected_at`,
		userID, messageID, languageCode, nowUnix())
	return err
}

// UpdateMessageSecurityState stores plugin-detected encrypted/signed metadata discovered while parsing raw messages.
func (s *Store) UpdateMessageSecurityState(ctx context.Context, userID, messageID int64, encrypted, signed bool) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET is_encrypted = ?, is_signed = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(encrypted), boolInt(signed), nowUnix(), userID, messageID)
	return err
}
