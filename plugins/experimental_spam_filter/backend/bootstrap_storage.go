package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const pendingMoveMaximumPerUser = 2000

func getBootstrap(ctx context.Context, db *sql.DB, userID int64) (bootstrapRecord, error) {
	var record bootstrapRecord
	err := db.QueryRowContext(ctx, `SELECT status, cutoff_at, candidate_spam, candidate_ham,
		examined, accepted_spam, accepted_ham, rejected, current_mailbox, last_error,
		started_at, updated_at, completed_at
		FROM plugin_experimental_spam_bootstraps WHERE user_id = ?`, userID).
		Scan(&record.Status, &record.CutoffAt, &record.CandidateSpam, &record.CandidateHam,
			&record.Examined, &record.AcceptedSpam, &record.AcceptedHam, &record.Rejected,
			&record.CurrentMailbox, &record.LastError, &record.StartedAt, &record.UpdatedAt,
			&record.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return bootstrapRecord{Status: "idle"}, nil
	}
	return record, err
}

func startBootstrapRecord(ctx context.Context, db *sql.DB, userID int64, cutoff time.Time, spamCandidates, hamCandidates int) error {
	now := time.Now().UTC().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_bootstraps
		(user_id, status, cutoff_at, candidate_spam, candidate_ham, examined,
		 accepted_spam, accepted_ham, rejected, current_mailbox, last_error,
		 started_at, updated_at, completed_at)
		VALUES (?, 'running', ?, ?, ?, 0, 0, 0, 0, '', '', ?, ?, 0)
		ON CONFLICT(user_id) DO UPDATE SET
		 status = 'running', cutoff_at = excluded.cutoff_at,
		 candidate_spam = excluded.candidate_spam, candidate_ham = excluded.candidate_ham,
		 examined = 0, accepted_spam = 0, accepted_ham = 0, rejected = 0,
		 current_mailbox = '', last_error = '', started_at = excluded.started_at,
		 updated_at = excluded.updated_at, completed_at = 0`,
		userID, cutoff.UTC().Unix(), spamCandidates, hamCandidates, now, now)
	return err
}

func updateBootstrapRecord(ctx context.Context, db *sql.DB, userID int64, examined, acceptedSpam, acceptedHam, rejected int, mailbox, lastError string) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_experimental_spam_bootstraps SET
		examined = ?, accepted_spam = ?, accepted_ham = ?, rejected = ?,
		current_mailbox = ?, last_error = ?, updated_at = ? WHERE user_id = ?`,
		examined, acceptedSpam, acceptedHam, rejected, boundedBootstrapText(mailbox, 160),
		safeError(lastError), time.Now().UTC().Unix(), userID)
	return err
}

func updateBootstrapCandidateCounts(ctx context.Context, db *sql.DB, userID int64, spam, ham int) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_experimental_spam_bootstraps
		SET candidate_spam = ?, candidate_ham = ?, updated_at = ? WHERE user_id = ?`,
		spam, ham, time.Now().UTC().Unix(), userID)
	return err
}

func finishBootstrapRecord(ctx context.Context, db *sql.DB, userID int64, status, lastError string) error {
	if status != "complete" && status != "failed" && status != "cancelled" {
		status = "failed"
	}
	now := time.Now().UTC().Unix()
	_, err := db.ExecContext(ctx, `UPDATE plugin_experimental_spam_bootstraps SET
		status = ?, current_mailbox = '', last_error = ?, updated_at = ?, completed_at = ?
		WHERE user_id = ?`, status, safeError(lastError), now, now, userID)
	return err
}

func resetBootstrapRecord(ctx context.Context, db *sql.DB, userID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_bootstraps WHERE user_id = ?`, userID)
	return err
}

func boundedBootstrapText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return boundedText(value, limit)
}

func savePendingMoveLabel(ctx context.Context, db *sql.DB, userID, accountID int64, identityKey, label string, sourceMailboxID, destinationMailboxID int64, now time.Time) error {
	identityKey = boundedBootstrapText(identityKey, 320)
	label = strings.ToLower(strings.TrimSpace(label))
	if userID <= 0 || accountID <= 0 || identityKey == "" || (label != feedbackSpam && label != feedbackHam) {
		return errors.New("pending move label is invalid")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND expires_at <= ?`, userID, now.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_experimental_spam_pending_move_labels
		(user_id, account_id, identity_key, label, source_mailbox_id,
		 destination_mailbox_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, account_id, identity_key) DO UPDATE SET
		 label = excluded.label, source_mailbox_id = excluded.source_mailbox_id,
		 destination_mailbox_id = excluded.destination_mailbox_id,
		 created_at = excluded.created_at, expires_at = excluded.expires_at`,
		userID, accountID, identityKey, label, sourceMailboxID, destinationMailboxID,
		now.Unix(), now.Add(7*24*time.Hour).Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND rowid IN (
			SELECT rowid FROM plugin_experimental_spam_pending_move_labels
			WHERE user_id = ?
			ORDER BY created_at DESC, account_id DESC, identity_key DESC
			LIMIT -1 OFFSET ?
		)`, userID, userID, pendingMoveMaximumPerUser); err != nil {
		return err
	}
	return tx.Commit()
}

func consumePendingMoveLabel(ctx context.Context, tx *sql.Tx, userID, accountID int64, identityKey string, now time.Time) (string, error) {
	identityKey = boundedBootstrapText(identityKey, 320)
	if userID <= 0 || accountID <= 0 || identityKey == "" {
		return "", nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND expires_at <= ?`, userID, now.UTC().Unix()); err != nil {
		return "", err
	}
	var label string
	err := tx.QueryRowContext(ctx, `SELECT label FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND account_id = ? AND identity_key = ? AND expires_at > ?`,
		userID, accountID, identityKey, now.UTC().Unix()).Scan(&label)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_experimental_spam_pending_move_labels
		WHERE user_id = ? AND account_id = ? AND identity_key = ?`, userID, accountID, identityKey); err != nil {
		return "", err
	}
	return label, nil
}
