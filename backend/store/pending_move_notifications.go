// File overview: Tenant-scoped markers that prevent mailbox moves from appearing as new mail.

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const pendingMoveNotificationTTL = 24 * time.Hour

// CreatePendingMoveNotification stages one suppression before an IMAP move.
// The raw digest and account scope come from the source message rather than a
// caller-provided value, and the destination must belong to that same account.
func (s *Store) CreatePendingMoveNotification(ctx context.Context, userID, sourceMessageID, destinationMailboxID int64) (int64, error) {
	if userID <= 0 || sourceMessageID <= 0 || destinationMailboxID <= 0 {
		return 0, fmt.Errorf("invalid pending move notification scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	now := nowUnix()
	result, err := db.ExecContext(ctx, `INSERT INTO pending_move_notifications
		(user_id, account_id, destination_mailbox_id, raw_sha256, created_at, expires_at)
		SELECT message.user_id, message.account_id, destination.id, blob.sha256, ?, ?
		FROM messages AS message
		JOIN blobs AS blob
			ON blob.user_id = message.user_id AND blob.id = message.blob_id
		JOIN mailboxes AS destination
			ON destination.user_id = message.user_id AND destination.account_id = message.account_id
		WHERE message.user_id = ? AND message.id = ? AND destination.id = ?
			AND blob.sha256 <> ''`,
		now, now+int64(pendingMoveNotificationTTL/time.Second), userID, sourceMessageID, destinationMailboxID)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, ErrNotFound
	}
	return result.LastInsertId()
}

// DeletePendingMoveNotification removes a staged marker when the remote move
// fails. The user predicate prevents one tenant from cancelling another's move.
func (s *Store) DeletePendingMoveNotification(ctx context.Context, userID, id int64) error {
	if userID <= 0 || id <= 0 {
		return fmt.Errorf("invalid pending move notification scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM pending_move_notifications WHERE user_id = ? AND id = ?`, userID, id)
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

// consumePendingMoveNotification marks one exact pending move as consumed by
// this fetched destination message. A consumed marker also suppresses retries
// for that message while leaving identical markers available for other moves.
func consumePendingMoveNotification(ctx context.Context, tx *sql.Tx, userID, messageID, now int64) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_move_notifications
		WHERE user_id = ? AND expires_at <= ?`, userID, now); err != nil {
		return false, err
	}

	var existing int64
	err := tx.QueryRowContext(ctx, `SELECT pending.id
		FROM pending_move_notifications AS pending
		JOIN messages AS message
			ON message.user_id = pending.user_id
			AND message.account_id = pending.account_id
			AND message.mailbox_id = pending.destination_mailbox_id
		JOIN blobs AS blob
			ON blob.user_id = message.user_id AND blob.id = message.blob_id
		WHERE message.user_id = ? AND message.id = ?
			AND pending.consumed_message_id = message.id
			AND pending.raw_sha256 = blob.sha256 COLLATE BINARY
			AND pending.expires_at > ?
		LIMIT 1`, userID, messageID, now).Scan(&existing)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}

	result, err := tx.ExecContext(ctx, `UPDATE pending_move_notifications
		SET consumed_message_id = ?, consumed_at = ?
		WHERE id = (
			SELECT pending.id
			FROM pending_move_notifications AS pending
			JOIN messages AS message
				ON message.user_id = pending.user_id
				AND message.account_id = pending.account_id
				AND message.mailbox_id = pending.destination_mailbox_id
			JOIN blobs AS blob
				ON blob.user_id = message.user_id AND blob.id = message.blob_id
			WHERE message.user_id = ? AND message.id = ?
				AND pending.consumed_message_id IS NULL
				AND pending.raw_sha256 = blob.sha256 COLLATE BINARY
				AND pending.expires_at > ?
			ORDER BY pending.id
			LIMIT 1
		)
		AND user_id = ? AND consumed_message_id IS NULL`,
		messageID, now, userID, messageID, now, userID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}
