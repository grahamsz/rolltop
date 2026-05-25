// File overview: Message language and attachment indexing metadata persistence.

package store

import (
	"context"
	"strings"
)

// ListMessagesNeedingAttachmentIndex returns messages whose raw bodies still need attachment text extraction.
func (s *Store) ListMessagesNeedingAttachmentIndex(ctx context.Context, userID int64, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND attachment_indexed_at = 0 ORDER BY id LIMIT ?`, userID, limit)
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
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
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
			date_unix, internal_date_unix, uid, size, blob_path, body_text, body_html, is_read, read_sync_pending, is_starred, star_sync_pending, has_attachments, attachment_indexed_at, created_at, updated_at
		FROM messages WHERE user_id = ? AND star_sync_pending = 1 ORDER BY updated_at LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// MarkMessageAttachmentIndexed records that attachment text extraction ran for a message.
func (s *Store) MarkMessageAttachmentIndexed(ctx context.Context, userID, messageID int64, hasAttachments bool) error {
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `UPDATE messages SET has_attachments = ?, attachment_indexed_at = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(hasAttachments), nowUnix(), nowUnix(), userID, messageID)
	return err
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
