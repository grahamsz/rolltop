// File overview: Short-lived, tenant-scoped fingerprints recorded atomically before reconciled source deletion.

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RecordExpungedMessageFingerprint snapshots a source while it still exists.
// Reconciliation should prefer DeleteMessagesMissingUIDsAndRecordExpunges so
// insertion and message deletion share one transaction.
func (s *Store) RecordExpungedMessageFingerprint(ctx context.Context, userID, messageID int64, canonicalSHA string) error {
	if userID <= 0 || messageID <= 0 {
		return errors.New("invalid expunged fingerprint scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recordExpungedMessageFingerprintTx(ctx, tx, userID, messageID, canonicalSHA, nowUnix()); err != nil {
		return err
	}
	return tx.Commit()
}

func recordExpungedMessageFingerprintTx(ctx context.Context, tx *sql.Tx, userID, messageID int64, canonicalSHA string, now int64) error {
	var accountID, mailboxID, sourceUID, sourceUIDValidity, mailboxUIDValidity, internalDate, size int64
	var rawSHA, storedCanonical, messageIDHeader, storedMessageIDHash, mailboxName, mailboxRole string
	err := tx.QueryRowContext(ctx, `SELECT message.account_id, message.mailbox_id,
		message.uid, blob.sha256, message.canonical_sha256, message.message_id_header,
		message.message_id_hash, message.internal_date_unix, message.size, message.uid_validity,
		mailbox.uidvalidity, mailbox.name, mailbox.role
		FROM messages message
		JOIN blobs blob ON blob.user_id = message.user_id AND blob.id = message.blob_id
		JOIN mailboxes mailbox ON mailbox.user_id = message.user_id AND mailbox.id = message.mailbox_id
		WHERE message.user_id = ? AND message.id = ?`, userID, messageID).
		Scan(&accountID, &mailboxID, &sourceUID, &rawSHA, &storedCanonical,
			&messageIDHeader, &storedMessageIDHash, &internalDate, &size, &sourceUIDValidity,
			&mailboxUIDValidity, &mailboxName, &mailboxRole)
	if err != nil {
		return err
	}
	if sourceUIDValidity <= 0 || mailboxUIDValidity <= 0 || sourceUIDValidity != mailboxUIDValidity {
		return nil
	}
	if normalizeMailboxRole(mailboxRole) == "all" || isAllMailMailboxName(mailboxName) {
		return nil
	}
	var transferID int64
	var transferState, destinationName, destinationRole string
	transferErr := tx.QueryRowContext(ctx, `SELECT transfer.id, transfer.state, destination.name, destination.role
		FROM message_transfers transfer
		JOIN mailboxes destination ON destination.user_id = transfer.user_id
			AND destination.id = transfer.destination_mailbox_id
		WHERE transfer.user_id = ? AND transfer.source_account_id = ?
			AND transfer.source_mailbox_id = ? AND transfer.source_uid = ?
			AND transfer.source_uid_validity = ? AND transfer.operation_kind = 'move'
			AND (transfer.state IN ('succeeded', 'consumed')
				OR (transfer.state = 'pending' AND transfer.dispatched_at > 0))
		ORDER BY CASE transfer.state WHEN 'consumed' THEN 1 WHEN 'succeeded' THEN 2 ELSE 3 END,
			transfer.id DESC LIMIT 1`, userID, accountID, mailboxID, sourceUID,
		sourceUIDValidity).Scan(&transferID, &transferState, &destinationName, &destinationRole)
	if transferErr == nil {
		destinationIsInbox := normalizeMailboxRole(destinationRole) == "inbox" ||
			strings.EqualFold(strings.TrimSpace(destinationName), "INBOX")
		if transferState == "pending" {
			if destinationIsInbox {
				// Exact source disappearance under the same UIDVALIDITY resolves an
				// outcome-unknown MOVE. Promote the destination-linked journal instead
				// of relying on a short-lived generic expunge tombstone.
				_, err := tx.ExecContext(ctx, `UPDATE message_transfers SET state = 'succeeded',
					completed_at = CASE WHEN completed_at = 0 THEN ? ELSE completed_at END,
					updated_at = ? WHERE user_id = ? AND id = ? AND state = 'pending'
						AND dispatched_at > 0`,
					now, now, userID, transferID)
				return err
			}
			// The source deletion is the local completion for a dispatched MOVE to
			// a non-notification mailbox. Retire it atomically; no Inbox arrival will
			// ever consume the transfer.
			_, err := tx.ExecContext(ctx, `UPDATE message_transfers SET state = 'consumed',
				consumed_at = ?, completed_at = CASE WHEN completed_at = 0 THEN ? ELSE completed_at END,
				expires_at = ?, updated_at = ?
				WHERE user_id = ? AND id = ? AND state = 'pending' AND dispatched_at > 0`,
				now, now, now+int64(messageTransferTTL/time.Second), now, userID, transferID)
			return err
		}
		// Completed transfers already carry destination-linked evidence. For an
		// unknown outcome to a non-Inbox destination, never create a generic token
		// capable of suppressing an unrelated Inbox delivery.
		return nil
	} else if !errors.Is(transferErr, sql.ErrNoRows) {
		return transferErr
	}
	canonicalSHA = strings.TrimSpace(canonicalSHA)
	if storedCanonical != "" && canonicalSHA != "" && storedCanonical != canonicalSHA {
		return errors.New("expunged message fingerprint mismatch")
	}
	if canonicalSHA == "" {
		canonicalSHA = storedCanonical
	}
	messageIDHash := HashedMessageID(messageIDHeader)
	if storedMessageIDHash != "" {
		messageIDHash = storedMessageIDHash
	}
	if err := validateHexFingerprint(canonicalSHA); err != nil {
		return err
	}
	if err := validateHexFingerprint(messageIDHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET canonical_sha256 = ?, message_id_hash = ?
		WHERE user_id = ? AND id = ?`, canonicalSHA, messageIDHash, userID, messageID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO expunged_message_fingerprints
		(user_id, account_id, source_mailbox_id, source_uid, source_uid_validity, raw_sha256, canonical_sha256,
		 message_id_hash, internal_date_unix, message_size, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, userID, accountID, mailboxID, sourceUID, sourceUIDValidity,
		rawSHA, canonicalSHA, messageIDHash, internalDate, size, now,
		now+int64(expungedFingerprintTTL/time.Second))
	return err
}

func isAllMailMailboxName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "all mail", "[gmail]/all mail", "allmail":
		return true
	default:
		return false
	}
}
