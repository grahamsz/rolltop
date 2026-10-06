// File overview: CardDAV sync routine persistence and run bookkeeping.

package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const routineColumns = `id, user_id, name, enabled, server_url, username,
	encrypted_password, addressbook_url, poll_interval_minutes, sync_token,
	state, last_error, last_started_at, last_completed_at, synced_total,
	created_at, updated_at`

type routine struct {
	ID                int64
	UserID            int64
	Name              string
	Enabled           bool
	ServerURL         string
	Username          string
	EncryptedPassword string
	AddressbookURL    string
	PollIntervalMin   int
	SyncToken         string
	State             string
	LastError         string
	LastStartedAt     time.Time
	LastCompletedAt   time.Time
	SyncedTotal       int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type runRecord struct {
	ID          int64  `json:"id"`
	RoutineID   int64  `json:"routine_id"`
	Trigger     string `json:"trigger"`
	Status      string `json:"status"`
	Scanned     int64  `json:"scanned"`
	Added       int64  `json:"added"`
	Updated     int64  `json:"updated"`
	Deleted     int64  `json:"deleted"`
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
	var enabled int
	var started, completed, created, updated int64
	err := row.Scan(
		&out.ID, &out.UserID, &out.Name, &enabled, &out.ServerURL, &out.Username,
		&out.EncryptedPassword, &out.AddressbookURL, &out.PollIntervalMin, &out.SyncToken,
		&out.State, &out.LastError, &started, &completed, &out.SyncedTotal,
		&created, &updated,
	)
	out.Enabled = enabled != 0
	out.LastStartedAt = unixTime(started)
	out.LastCompletedAt = unixTime(completed)
	out.CreatedAt = unixTime(created)
	out.UpdatedAt = unixTime(updated)
	return out, err
}

func getRoutine(ctx context.Context, db *sql.DB, userID, routineID int64) (routine, error) {
	if userID <= 0 || routineID <= 0 {
		return routine{}, sql.ErrNoRows
	}
	return scanRoutine(db.QueryRowContext(ctx, `SELECT `+routineColumns+`
		FROM plugin_carddav_sync_routines WHERE user_id = ? AND id = ?`, userID, routineID))
}

func listRoutines(ctx context.Context, db *sql.DB, userID int64, enabledOnly bool) ([]routine, error) {
	query := `SELECT ` + routineColumns + ` FROM plugin_carddav_sync_routines WHERE user_id = ?`
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
	res, err := db.ExecContext(ctx, `DELETE FROM plugin_carddav_sync_routines WHERE user_id = ? AND id = ?`, userID, routineID)
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
	res, err := db.ExecContext(ctx, `INSERT INTO plugin_carddav_sync_runs
		(user_id, routine_id, trigger, status, started_at, created_at)
		VALUES (?, ?, ?, 'running', ?, ?)`, userID, routineID, trigger, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func finishRun(ctx context.Context, db *sql.DB, userID, runID int64, status, message string, scanned, added, updated, deleted int64) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_runs
		SET status = ?, error = ?, scanned = ?, added = ?, updated = ?, deleted = ?, completed_at = ?
		WHERE user_id = ? AND id = ?`,
		status, message, scanned, added, updated, deleted, time.Now().UTC().Unix(), userID, runID)
	return err
}

// recoverInterruptedRuns clears run and routine state left behind when the
// server stopped or the plugin reloaded mid-sync.
func recoverInterruptedRuns(ctx context.Context, db *sql.DB, userID int64) (int64, error) {
	now := time.Now().UTC().Unix()
	res, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_runs
		SET status = 'interrupted', error = '', completed_at = ?
		WHERE user_id = ? AND status IN ('running', 'queued')`, now, userID)
	if err != nil {
		return 0, err
	}
	interrupted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_routines
		SET state = CASE WHEN enabled = 1 THEN 'queued' ELSE 'paused' END, updated_at = ?
		WHERE user_id = ? AND state = 'syncing'`, now, userID); err != nil {
		return interrupted, err
	}
	return interrupted, nil
}

func latestRun(ctx context.Context, db *sql.DB, userID, routineID int64) (*runRecord, error) {
	var out runRecord
	err := db.QueryRowContext(ctx, `SELECT id, routine_id, trigger, status, scanned, added,
		updated, deleted, error, started_at, completed_at, created_at
		FROM plugin_carddav_sync_runs WHERE user_id = ? AND routine_id = ?
		ORDER BY id DESC LIMIT 1`, userID, routineID).Scan(
		&out.ID, &out.RoutineID, &out.Trigger, &out.Status, &out.Scanned, &out.Added,
		&out.Updated, &out.Deleted, &out.Error, &out.StartedAt, &out.CompletedAt, &out.CreatedAt,
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
	rows, err := db.QueryContext(ctx, `SELECT id, routine_id, trigger, status, scanned, added,
		updated, deleted, error, started_at, completed_at, created_at
		FROM plugin_carddav_sync_runs WHERE user_id = ? AND routine_id = ?
		ORDER BY id DESC LIMIT ?`, userID, routineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]runRecord, 0)
	for rows.Next() {
		var item runRecord
		if err := rows.Scan(&item.ID, &item.RoutineID, &item.Trigger, &item.Status, &item.Scanned,
			&item.Added, &item.Updated, &item.Deleted, &item.Error, &item.StartedAt,
			&item.CompletedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func beginRoutineRun(ctx context.Context, db *sql.DB, item routine) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_routines SET
		state = 'syncing', last_error = '', last_started_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`, time.Now().UTC().Unix(), item.UserID, item.ID)
	return err
}

func completeRoutineRun(ctx context.Context, db *sql.DB, item routine, syncToken string, added, updated, deleted int64) error {
	now := time.Now().UTC().Unix()
	_, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_routines SET
		state = 'watching', last_error = '', last_completed_at = ?, sync_token = ?,
		synced_total = synced_total + ?, updated_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`,
		now, syncToken, added+updated, now, item.UserID, item.ID)
	return err
}

func failRoutineRun(ctx context.Context, db *sql.DB, item routine, message string, retry bool) error {
	state := "error"
	if retry {
		state = "retrying"
	}
	_, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_routines SET
		state = ?, last_error = ?, updated_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`,
		state, message, time.Now().UTC().Unix(), item.UserID, item.ID)
	return err
}

// suspendRoutineForCredentialError stops retries that cannot recover on their
// own. Continuing to dial with an invalid password creates noisy logs and
// competes with the main mailbox mirror for SQLite writes.
func suspendRoutineForCredentialError(ctx context.Context, db *sql.DB, item routine, message string) error {
	_, err := db.ExecContext(ctx, `UPDATE plugin_carddav_sync_routines SET
		enabled = 0, state = 'needs_credentials', last_error = ?, updated_at = ?
		WHERE user_id = ? AND id = ? AND enabled = 1`,
		message, time.Now().UTC().Unix(), item.UserID, item.ID)
	return err
}

// mappingRow is one vCard UID to Rolltop contact link. OwnsContact is true
// only when the plugin created the contact; it is provenance, not permission
// to delete local data.
type mappingRow struct {
	VCardUID    string
	Href        string
	ETag        string
	ContactID   int64
	OwnsContact bool
}

func getMapping(ctx context.Context, db *sql.DB, userID, routineID int64, vcardUID string) (mappingRow, error) {
	var out mappingRow
	var owns int
	err := db.QueryRowContext(ctx, `SELECT vcard_uid, href, etag, contact_id, owns_contact
		FROM plugin_carddav_sync_contacts WHERE user_id = ? AND routine_id = ? AND vcard_uid = ?`,
		userID, routineID, vcardUID).Scan(&out.VCardUID, &out.Href, &out.ETag, &out.ContactID, &owns)
	out.OwnsContact = owns != 0
	return out, err
}

func listMappings(ctx context.Context, db *sql.DB, userID, routineID int64) (map[string]mappingRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT vcard_uid, href, etag, contact_id, owns_contact
		FROM plugin_carddav_sync_contacts WHERE user_id = ? AND routine_id = ?`, userID, routineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]mappingRow)
	for rows.Next() {
		var row mappingRow
		var owns int
		if err := rows.Scan(&row.VCardUID, &row.Href, &row.ETag, &row.ContactID, &owns); err != nil {
			return nil, err
		}
		row.OwnsContact = owns != 0
		out[row.VCardUID] = row
	}
	return out, rows.Err()
}

func upsertMapping(ctx context.Context, db *sql.DB, userID, routineID int64, row mappingRow) error {
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_carddav_sync_contacts
		(user_id, routine_id, vcard_uid, href, etag, contact_id, owns_contact, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, routine_id, vcard_uid)
		DO UPDATE SET href = excluded.href, etag = excluded.etag,
			contact_id = excluded.contact_id, owns_contact = excluded.owns_contact,
			updated_at = excluded.updated_at`,
		userID, routineID, row.VCardUID, row.Href, row.ETag, row.ContactID,
		boolInt(row.OwnsContact), time.Now().UTC().Unix())
	return err
}

func deleteMapping(ctx context.Context, db *sql.DB, userID, routineID int64, vcardUID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_carddav_sync_contacts
		WHERE user_id = ? AND routine_id = ? AND vcard_uid = ?`, userID, routineID, vcardUID)
	return err
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

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Unix()
}

func isUniqueError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

func sourceIdentityChanged(a, b routine) bool {
	return strings.TrimSpace(a.ServerURL) != strings.TrimSpace(b.ServerURL) ||
		strings.TrimSpace(a.Username) != strings.TrimSpace(b.Username) ||
		strings.TrimSpace(a.AddressbookURL) != strings.TrimSpace(b.AddressbookURL)
}
