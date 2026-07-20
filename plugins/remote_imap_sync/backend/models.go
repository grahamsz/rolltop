package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const routineColumns = `id, user_id, name, enabled, source_provider, source_host,
	source_port, source_username, encrypted_source_password, source_use_tls,
	source_mailbox, destination_account_id, destination_mailbox_id, after_date,
	marker_secret, source_uidvalidity, last_source_uid, state, last_error,
	last_started_at, last_completed_at, last_activity_at, next_retry_at,
	transferred_total, skipped_total, created_at, updated_at`

type routine struct {
	ID                      int64
	UserID                  int64
	Name                    string
	Enabled                 bool
	SourceProvider          string
	SourceHost              string
	SourcePort              int
	SourceUsername          string
	EncryptedSourcePassword string
	SourceUseTLS            bool
	SourceMailbox           string
	DestinationAccountID    int64
	DestinationMailboxID    int64
	AfterDate               time.Time
	MarkerSecret            string
	SourceUIDValidity       uint32
	LastSourceUID           uint32
	State                   string
	LastError               string
	LastStartedAt           time.Time
	LastCompletedAt         time.Time
	LastActivityAt          time.Time
	NextRetryAt             time.Time
	TransferredTotal        int64
	SkippedTotal            int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type runRecord struct {
	ID          int64  `json:"id"`
	RoutineID   int64  `json:"routine_id"`
	Trigger     string `json:"trigger"`
	Status      string `json:"status"`
	Scanned     int64  `json:"scanned"`
	Transferred int64  `json:"transferred"`
	Skipped     int64  `json:"skipped"`
	CurrentUID  uint32 `json:"current_uid"`
	Error       string `json:"error"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at"`
	CreatedAt   int64  `json:"created_at"`
}

type rowScanner interface {
	Scan(...any) error
}

func scanRoutine(row rowScanner) (routine, error) {
	var out routine
	var enabled, useTLS int
	var afterDate, started, completed, activity, retry, created, updated int64
	err := row.Scan(
		&out.ID, &out.UserID, &out.Name, &enabled, &out.SourceProvider, &out.SourceHost,
		&out.SourcePort, &out.SourceUsername, &out.EncryptedSourcePassword, &useTLS,
		&out.SourceMailbox, &out.DestinationAccountID, &out.DestinationMailboxID, &afterDate,
		&out.MarkerSecret, &out.SourceUIDValidity, &out.LastSourceUID, &out.State, &out.LastError,
		&started, &completed, &activity, &retry, &out.TransferredTotal, &out.SkippedTotal,
		&created, &updated,
	)
	out.Enabled = enabled != 0
	out.SourceUseTLS = useTLS != 0
	out.AfterDate = unixTime(afterDate)
	out.LastStartedAt = unixTime(started)
	out.LastCompletedAt = unixTime(completed)
	out.LastActivityAt = unixTime(activity)
	out.NextRetryAt = unixTime(retry)
	out.CreatedAt = unixTime(created)
	out.UpdatedAt = unixTime(updated)
	return out, err
}

func getRoutine(ctx context.Context, db *sql.DB, userID, routineID int64) (routine, error) {
	if userID <= 0 || routineID <= 0 {
		return routine{}, sql.ErrNoRows
	}
	return scanRoutine(db.QueryRowContext(ctx, `SELECT `+routineColumns+`
		FROM plugin_remote_imap_sync_routines WHERE user_id = ? AND id = ?`, userID, routineID))
}

func listRoutines(ctx context.Context, db *sql.DB, userID int64, enabledOnly bool) ([]routine, error) {
	query := `SELECT ` + routineColumns + ` FROM plugin_remote_imap_sync_routines WHERE user_id = ?`
	if enabledOnly {
		query += ` AND enabled = 1`
	}
	query += ` ORDER BY lower(name), id`
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]routine, 0)
	for rows.Next() {
		item, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func deleteRoutine(ctx context.Context, db *sql.DB, userID, routineID int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM plugin_remote_imap_sync_routines WHERE user_id = ? AND id = ?`, userID, routineID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func setRoutineEnabled(ctx context.Context, db *sql.DB, userID, routineID int64, enabled bool) error {
	state := "paused"
	if enabled {
		state = "queued"
	}
	res, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines
		SET enabled = ?, state = ?, last_error = '', next_retry_at = 0, updated_at = ?
		WHERE user_id = ? AND id = ?`, boolInt(enabled), state, time.Now().UTC().Unix(), userID, routineID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func createRun(ctx context.Context, db *sql.DB, userID, routineID int64, trigger string) (int64, error) {
	now := time.Now().UTC().Unix()
	res, err := db.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_runs
		(user_id, routine_id, trigger, status, started_at, created_at)
		VALUES (?, ?, ?, 'running', ?, ?)`, userID, routineID, trigger, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func updateRunProgress(ctx context.Context, db *sql.DB, userID, runID int64, scanned, transferred, skipped int64, currentUID uint32) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_runs
		SET scanned = ?, transferred = ?, skipped = ?, current_uid = ?
		WHERE user_id = ? AND id = ?`, scanned, transferred, skipped, currentUID, userID, runID)
	return err
}

func finishRun(ctx context.Context, db *sql.DB, userID, runID int64, status, message string) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_runs
		SET status = ?, error = ?, completed_at = ? WHERE user_id = ? AND id = ?`,
		status, message, time.Now().UTC().Unix(), userID, runID)
	return err
}

func latestRun(ctx context.Context, db *sql.DB, userID, routineID int64) (*runRecord, error) {
	var out runRecord
	err := db.QueryRowContext(ctx, `SELECT id, routine_id, trigger, status, scanned, transferred,
		skipped, current_uid, error, started_at, completed_at, created_at
		FROM plugin_remote_imap_sync_runs WHERE user_id = ? AND routine_id = ?
		ORDER BY id DESC LIMIT 1`, userID, routineID).Scan(
		&out.ID, &out.RoutineID, &out.Trigger, &out.Status, &out.Scanned, &out.Transferred,
		&out.Skipped, &out.CurrentUID, &out.Error, &out.StartedAt, &out.CompletedAt, &out.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}

func recentRuns(ctx context.Context, db *sql.DB, userID, routineID int64, limit int) ([]runRecord, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `SELECT id, routine_id, trigger, status, scanned, transferred,
		skipped, current_uid, error, started_at, completed_at, created_at
		FROM plugin_remote_imap_sync_runs WHERE user_id = ? AND routine_id = ?
		ORDER BY id DESC LIMIT ?`, userID, routineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]runRecord, 0)
	for rows.Next() {
		var item runRecord
		if err := rows.Scan(&item.ID, &item.RoutineID, &item.Trigger, &item.Status, &item.Scanned,
			&item.Transferred, &item.Skipped, &item.CurrentUID, &item.Error, &item.StartedAt,
			&item.CompletedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func messageAlreadyHandled(ctx context.Context, db *sql.DB, userID, routineID int64, uidValidity, uid uint32, fingerprint string) (bool, error) {
	var exact, currentOccurrences, priorMaxOccurrences int
	err := db.QueryRowContext(ctx, `WITH matches AS (
			SELECT source_uidvalidity, source_uid
			FROM plugin_remote_imap_sync_messages
			WHERE user_id = ? AND routine_id = ?
			AND ((source_uidvalidity = ? AND source_uid = ?) OR source_fingerprint = ?)
		), prior_counts AS (
			SELECT COUNT(*) AS occurrences FROM matches
			WHERE source_uidvalidity <> ? GROUP BY source_uidvalidity
		)
		SELECT
			EXISTS(SELECT 1 FROM matches WHERE source_uidvalidity = ? AND source_uid = ?),
			(SELECT COUNT(*) FROM matches WHERE source_uidvalidity = ?),
			COALESCE((SELECT MAX(occurrences) FROM prior_counts), 0)`,
		userID, routineID, uidValidity, uid, fingerprint, uidValidity,
		uidValidity, uid, uidValidity).Scan(&exact, &currentOccurrences, &priorMaxOccurrences)
	if err != nil {
		return false, err
	}
	return exact != 0 || currentOccurrences < priorMaxOccurrences, nil
}

func recordHandledMessage(ctx context.Context, db *sql.DB, item routine, uidValidity, uid uint32, fingerprint, marker string, destinationUID uint32, status string) error {
	return recordHandledMessageAt(ctx, db, item, uidValidity, uid, fingerprint, marker, destinationUID, status, time.Time{}, 0, "")
}

func recordHandledMessageAt(ctx context.Context, db *sql.DB, item routine, uidValidity, uid uint32, fingerprint, marker string, destinationUID uint32, status string, copiedAt time.Time, destinationUIDValidity uint32, destinationSHA256 string) error {
	if status != "transferred" && status != "skipped" {
		return fmt.Errorf("invalid handled-message status %q", status)
	}
	now := time.Now().UTC().Unix()
	copiedUnix := now
	if !copiedAt.IsZero() {
		copiedUnix = copiedAt.UTC().Truncate(time.Second).Unix()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_messages
		(user_id, routine_id, source_uidvalidity, source_uid, source_fingerprint, marker,
		 destination_uid, status, copied_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		item.UserID, item.ID, uidValidity, uid, fingerprint, marker, destinationUID, status, copiedUnix)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if status == "transferred" && copiedUnix > 0 && destinationUIDValidity > 0 && destinationUID > 0 && validDestinationSHA256(destinationSHA256) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_remote_imap_sync_provenance
			(user_id, destination_account_id, destination_mailbox_id, destination_uidvalidity, destination_uid, destination_sha256, synced_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, destination_account_id, destination_mailbox_id, destination_uidvalidity, destination_uid)
			DO UPDATE SET synced_at = CASE
				WHEN excluded.destination_sha256 = plugin_remote_imap_sync_provenance.destination_sha256
				 AND excluded.synced_at < plugin_remote_imap_sync_provenance.synced_at THEN excluded.synced_at
				ELSE plugin_remote_imap_sync_provenance.synced_at
			END`, item.UserID, item.DestinationAccountID, item.DestinationMailboxID, destinationUIDValidity, destinationUID, destinationSHA256, copiedUnix); err != nil {
			return err
		}
	}
	transferred, skipped := 0, 0
	if inserted > 0 && status == "transferred" {
		transferred = 1
	} else if inserted > 0 {
		skipped = 1
	}
	result, err = tx.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		source_uidvalidity = ?, last_source_uid = CASE WHEN last_source_uid < ? THEN ? ELSE last_source_uid END,
		last_activity_at = ?, transferred_total = transferred_total + ?, skipped_total = skipped_total + ?,
		updated_at = ? WHERE user_id = ? AND id = ?`, uidValidity, uid, uid, now,
		transferred, skipped, now, item.UserID, item.ID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func validDestinationSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}

func beginRoutineRun(ctx context.Context, db *sql.DB, item routine) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		state = 'syncing', last_error = '', last_started_at = ?, next_retry_at = 0
		WHERE user_id = ? AND id = ? AND enabled = 1`, time.Now().UTC().Unix(), item.UserID, item.ID)
	return err
}

func completeRoutineRun(ctx context.Context, db *sql.DB, item routine) error {
	now := time.Now().UTC().Unix()
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		state = 'watching', last_error = '', last_completed_at = ?, next_retry_at = 0
		WHERE user_id = ? AND id = ? AND enabled = 1`, now, item.UserID, item.ID)
	return err
}

func failRoutineRun(ctx context.Context, db *sql.DB, item routine, message string, retryAt time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		state = 'retrying', last_error = ?, next_retry_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`, message, retryAt.UTC().Unix(), item.UserID, item.ID)
	return err
}

// suspendRoutineForCredentialError stops retries that cannot recover on their
// own. Continuing to dial with an invalid app password creates noisy logs and
// competes with the main mailbox mirror for SQLite writes.
func suspendRoutineForCredentialError(ctx context.Context, db *sql.DB, item routine, message string) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		enabled = 0, state = 'needs_credentials', last_error = ?, next_retry_at = 0,
		updated_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`,
		message, time.Now().UTC().Unix(), item.UserID, item.ID)
	return err
}

func isRemoteAuthenticationError(err error) bool {
	message := strings.ToLower(sanitizeRemoteError(err))
	return strings.Contains(message, "authentication failed") || strings.Contains(message, "invalid credentials")
}

func resetRoutineProgress(ctx context.Context, tx *sql.Tx, userID, routineID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_remote_imap_sync_messages WHERE user_id = ? AND routine_id = ?`, userID, routineID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE plugin_remote_imap_sync_routines SET
		source_uidvalidity = 0, last_source_uid = 0, transferred_total = 0,
		skipped_total = 0, last_error = '', next_retry_at = 0
		WHERE user_id = ? AND id = ?`, userID, routineID)
	return err
}

func newMarkerSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func isUniqueError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

func structuralChange(a, b routine) bool {
	return !strings.EqualFold(strings.TrimSpace(a.SourceHost), strings.TrimSpace(b.SourceHost)) ||
		a.SourcePort != b.SourcePort || a.SourceUsername != b.SourceUsername ||
		a.SourceUseTLS != b.SourceUseTLS || a.SourceMailbox != b.SourceMailbox ||
		a.DestinationAccountID != b.DestinationAccountID ||
		a.DestinationMailboxID != b.DestinationMailboxID || !a.AfterDate.Equal(b.AfterDate)
}

func validateRoutineRecord(item routine) error {
	if strings.TrimSpace(item.Name) == "" {
		return fmt.Errorf("routine name is required")
	}
	if strings.TrimSpace(item.SourceHost) == "" || strings.TrimSpace(item.SourceUsername) == "" {
		return fmt.Errorf("source host and username are required")
	}
	if item.SourcePort < 1 || item.SourcePort > 65535 {
		return fmt.Errorf("source port is invalid")
	}
	if strings.TrimSpace(item.SourceMailbox) == "" {
		return fmt.Errorf("source folder is required")
	}
	if item.DestinationAccountID <= 0 || item.DestinationMailboxID <= 0 {
		return fmt.Errorf("destination folder is required")
	}
	if !item.SourceUseTLS && !isLoopbackHost(item.SourceHost) {
		return fmt.Errorf("TLS is required for remote IMAP sources")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
