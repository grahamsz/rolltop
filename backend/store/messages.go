// File overview: Message creation, lookup, update, and conversation-thread persistence helpers.

package store

import (
	"context"
	"strings"
	"time"
)

// CreateMessage is the insert payload for a mirrored message and its mailbox location.
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

// CreateMessage inserts or updates a mirrored message row and its mailbox location.
func (s *Store) CreateMessage(ctx context.Context, m CreateMessage) (MessageRecord, error) {
	db, err := s.dataDB(ctx, m.UserID)
	if err != nil {
		return MessageRecord{}, err
	}
	ts := nowUnix()
	if strings.TrimSpace(m.ThreadKey) == "" {
		m.ThreadKey = ThreadKey(m.MessageIDHeader, m.InReplyTo, m.ReferencesHeader, m.Subject)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO messages
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
	if err := s.upsertPluginMessageLanguage(ctx, m.UserID, id, m.LanguageCode); err != nil {
		return MessageRecord{}, err
	}
	return s.GetMessageForUser(ctx, m.UserID, id)
}

// GetMessageByUID loads one message by account/mailbox UID inside a user scope.
func (s *Store) GetMessageByUID(ctx context.Context, userID, accountID, mailboxID int64, uid uint32) (MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MessageRecord{}, err
	}
	var m MessageRecord
	var dateUnix, internalUnix, indexedAt, created, updated int64
	err = db.QueryRowContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
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

// CreateLocation records that a message appears in a mailbox at a specific UID.
func (s *Store) CreateLocation(ctx context.Context, userID, messageID, mailboxID int64, uid uint32) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO locations (user_id, message_id, mailbox_id, uid, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, messageID, mailboxID, uid, nowUnix())
	return err
}

// GetMessageForUser loads one message by local ID inside a user scope.
func (s *Store) GetMessageForUser(ctx context.Context, userID, id int64) (MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MessageRecord{}, err
	}
	var m MessageRecord
	var dateUnix, internalUnix, indexedAt, created, updated int64
	err = db.QueryRowContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
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

// GetMessageByBlobIDForUser finds the message that owns a blob record for one user.
func (s *Store) GetMessageByBlobIDForUser(ctx context.Context, userID, blobID int64) (MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MessageRecord{}, err
	}
	var m MessageRecord
	var dateUnix, internalUnix, indexedAt, created, updated int64
	err = db.QueryRowContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
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

// DeleteMessageForUser removes one local message row and dependent data for a user.
func (s *Store) DeleteMessageForUser(ctx context.Context, userID, id int64) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM messages WHERE user_id = ? AND id = ?`, userID, id)
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

// DeleteMessagesMissingUIDs removes local messages no longer present in a remote mailbox UID set.
func (s *Store) DeleteMessagesMissingUIDs(ctx context.Context, userID, accountID, mailboxID int64, remoteUIDs []uint32) ([]MessageRecord, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	remote := make(map[uint32]bool, len(remoteUIDs))
	for _, uid := range remoteUIDs {
		remote[uid] = true
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, account_id, mailbox_id, blob_id, message_id_header, in_reply_to, references_header, thread_key, subject, language_code, from_addr, to_addr, cc_addr,
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
	tx, err := db.BeginTx(ctx, nil)
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
