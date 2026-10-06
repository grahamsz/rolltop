// File overview: User-scoped persistence for calendar invites. Invites are
// keyed by (user_id, ics_uid) and always hold the newest SEQUENCE seen;
// CalDAV settings are one row per user with the password encrypted by the
// Rolltop master key. Passwords never leave the backend except decrypted in
// memory while opening a CalDAV connection.

package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/plugins/calendar_invites/schema"
)

var errInviteNotFound = errors.New("invite not found")

// ensureCalInvitesSchema creates the plugin tables on a user database.
// Production installs run the catalog-registered migrations instead; tests
// call this directly on an in-memory database.
func ensureCalInvitesSchema(db *sql.DB) error {
	for _, migration := range schema.Migrations() {
		for _, statement := range migration.Statements {
			if _, err := db.Exec(statement); err != nil {
				return err
			}
		}
	}
	return nil
}

// invite statuses stored in plugin_calendar_invites_invites.status.
const (
	inviteStatusPending   = "pending"
	inviteStatusAccepted  = "accepted"
	inviteStatusTentative = "tentative"
	inviteStatusDeclined  = "declined"
)

// inviteRecord is one detected METHOD:REQUEST invitation.
type inviteRecord struct {
	ID            int64  `json:"id"`
	MessageID     int64  `json:"message_id"`
	ICSUID        string `json:"ics_uid"`
	Sequence      int    `json:"sequence"`
	Summary       string `json:"summary"`
	DTStart       string `json:"dtstart"`
	DTEnd         string `json:"dtend"`
	OrganizerAddr string `json:"organizer_address"`
	OrganizerName string `json:"organizer_name"`
	AttendeeAddr  string `json:"attendee_address"`
	AttendeeName  string `json:"attendee_name"`
	Status        string `json:"status"`
	HasICS        bool   `json:"has_ics"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

func nowUnix() int64 { return time.Now().Unix() }

// upsertInvite records a detected invitation. When a newer SEQUENCE arrives
// for the same UID the row is refreshed and the status returns to pending, so
// the user answers the latest version of the event. Older SEQUENCEs are
// ignored. The raw ICS is kept so RSVP and CalDAV push do not need the
// original message.
func upsertInvite(ctx context.Context, db *sql.DB, userID, messageID int64, ev icsEvent, matched icsAttendee) error {
	now := nowUnix()
	var existingSeq int
	var existingID int64
	err := db.QueryRowContext(ctx,
		`SELECT id, sequence FROM plugin_calendar_invites_invites WHERE user_id = ? AND ics_uid = ?`,
		userID, ev.UID).Scan(&existingID, &existingSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// insert below
	case err != nil:
		return err
	default:
		if ev.Sequence <= existingSeq {
			return nil
		}
		_, err = db.ExecContext(ctx, `
			UPDATE plugin_calendar_invites_invites
			SET message_id = ?, sequence = ?, summary = ?, dtstart = ?, dtend = ?,
			    organizer_address = ?, organizer_name = ?,
			    attendee_address = ?, attendee_name = ?,
			    status = ?, raw_ics = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			messageID, ev.Sequence, ev.Summary, ev.DTStart, ev.DTEnd,
			ev.OrganizerAddr, ev.OrganizerName,
			matched.Address, matched.Name,
			inviteStatusPending, icsPayload(ev), now,
			existingID, userID)
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO plugin_calendar_invites_invites
			(user_id, message_id, ics_uid, sequence, method, summary, dtstart, dtend,
			 organizer_address, organizer_name, attendee_address, attendee_name,
			 status, raw_ics, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, messageID, ev.UID, ev.Sequence, ev.Method, ev.Summary, ev.DTStart, ev.DTEnd,
		ev.OrganizerAddr, ev.OrganizerName, matched.Address, matched.Name,
		inviteStatusPending, icsPayload(ev), now, now)
	return err
}

// icsPayload re-renders the detected event as a standalone VCALENDAR so the
// stored copy is complete even if the original message carried several
// components. The METHOD is preserved so the payload stays a REQUEST.
func icsPayload(ev icsEvent) string {
	if ev.RawICS != "" {
		return ev.RawICS
	}
	var out strings.Builder
	out.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Rolltop//Calendar Invites//EN\r\n")
	if ev.Method != "" {
		out.WriteString("METHOD:" + ev.Method + "\r\n")
	}
	out.WriteString("BEGIN:VEVENT\r\n")
	out.WriteString("UID:" + ev.UID + "\r\n")
	out.WriteString("SEQUENCE:" + strconv.Itoa(ev.Sequence) + "\r\n")
	if ev.DTStamp != "" {
		out.WriteString("DTSTAMP:" + ev.DTStamp + "\r\n")
	}
	if ev.DTStart != "" {
		out.WriteString("DTSTART:" + ev.DTStart + "\r\n")
	}
	if ev.DTEnd != "" {
		out.WriteString("DTEND:" + ev.DTEnd + "\r\n")
	}
	if ev.Summary != "" {
		out.WriteString("SUMMARY:" + escapeICSText(ev.Summary) + "\r\n")
	}
	if ev.OrganizerAddr != "" {
		line := "ORGANIZER"
		if ev.OrganizerName != "" {
			line += ";CN=" + escapeICSText(ev.OrganizerName)
		}
		out.WriteString(line + ":mailto:" + ev.OrganizerAddr + "\r\n")
	}
	for _, att := range ev.Attendees {
		line := "ATTENDEE"
		if att.Name != "" {
			line += ";CN=" + escapeICSText(att.Name)
		}
		if att.PartStat != "" {
			line += ";PARTSTAT=" + att.PartStat
		}
		if att.Role != "" {
			line += ";ROLE=" + att.Role
		}
		out.WriteString(line + ":mailto:" + att.Address + "\r\n")
	}
	out.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return out.String()
}

func listInvites(ctx context.Context, db *sql.DB, userID int64) ([]inviteRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, message_id, ics_uid, sequence, summary, dtstart, dtend,
		       organizer_address, organizer_name,
		       attendee_address, attendee_name, status,
		       raw_ics IS NOT NULL AND raw_ics != '',
		       created_at, updated_at
		FROM plugin_calendar_invites_invites
		WHERE user_id = ?
		ORDER BY updated_at DESC, id DESC
		LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inviteRecord
	for rows.Next() {
		var rec inviteRecord
		if err := rows.Scan(&rec.ID, &rec.MessageID, &rec.ICSUID, &rec.Sequence, &rec.Summary,
			&rec.DTStart, &rec.DTEnd, &rec.OrganizerAddr, &rec.OrganizerName,
			&rec.AttendeeAddr, &rec.AttendeeName, &rec.Status, &rec.HasICS,
			&rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func getInvite(ctx context.Context, db *sql.DB, userID, id int64) (inviteRecord, error) {
	var rec inviteRecord
	err := db.QueryRowContext(ctx, `
		SELECT id, message_id, ics_uid, sequence, summary, dtstart, dtend,
		       organizer_address, organizer_name,
		       attendee_address, attendee_name, status,
		       raw_ics IS NOT NULL AND raw_ics != '',
		       created_at, updated_at
		FROM plugin_calendar_invites_invites
		WHERE user_id = ? AND id = ?`, userID, id).Scan(
		&rec.ID, &rec.MessageID, &rec.ICSUID, &rec.Sequence, &rec.Summary,
		&rec.DTStart, &rec.DTEnd, &rec.OrganizerAddr, &rec.OrganizerName,
		&rec.AttendeeAddr, &rec.AttendeeName, &rec.Status, &rec.HasICS,
		&rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return inviteRecord{}, errInviteNotFound
	}
	return rec, err
}

// getInviteByUID fetches an invite by its ICS UID, used by tests and by the
// import path when correlating against existing rows.
func getInviteByUID(ctx context.Context, db *sql.DB, userID int64, uid string) (inviteRecord, error) {
	var rec inviteRecord
	err := db.QueryRowContext(ctx, `
		SELECT id, message_id, ics_uid, sequence, summary, dtstart, dtend,
		       organizer_address, organizer_name,
		       attendee_address, attendee_name, status,
		       raw_ics IS NOT NULL AND raw_ics != '',
		       created_at, updated_at
		FROM plugin_calendar_invites_invites
		WHERE user_id = ? AND ics_uid = ?`, userID, uid).Scan(
		&rec.ID, &rec.MessageID, &rec.ICSUID, &rec.Sequence, &rec.Summary,
		&rec.DTStart, &rec.DTEnd, &rec.OrganizerAddr, &rec.OrganizerName,
		&rec.AttendeeAddr, &rec.AttendeeName, &rec.Status, &rec.HasICS,
		&rec.CreatedAt, &rec.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return inviteRecord{}, errInviteNotFound
	}
	return rec, err
}

// inviteRawICS loads the stored ICS payload for RSVP and CalDAV push.
func inviteRawICS(ctx context.Context, db *sql.DB, userID, id int64) ([]byte, error) {
	var raw string
	err := db.QueryRowContext(ctx,
		`SELECT raw_ics FROM plugin_calendar_invites_invites WHERE user_id = ? AND id = ?`,
		userID, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errInviteNotFound
	}
	if err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

func setInviteStatus(ctx context.Context, db *sql.DB, userID, id int64, status string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE plugin_calendar_invites_invites SET status = ?, updated_at = ?, rsvp_revision = rsvp_revision + 1 WHERE user_id = ? AND id = ?`,
		status, nowUnix(), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errInviteNotFound
	}
	return nil
}

// caldavConfig is one user's CalDAV connection for pushing invites.
type caldavConfig struct {
	ServerURL   string `json:"server_url"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
	CalendarURL string `json:"calendar_url"`
	Enabled     bool   `json:"enabled"`
	LastError   string `json:"last_error"`
}

func getCalDAVConfig(ctx context.Context, db *sql.DB, userID int64) (caldavConfig, error) {
	var cfg caldavConfig
	var encrypted string
	var enabled int
	err := db.QueryRowContext(ctx, `
		SELECT server_url, username, encrypted_password, calendar_url, enabled, last_error
		FROM plugin_calendar_invites_caldav WHERE user_id = ?`, userID).Scan(
		&cfg.ServerURL, &cfg.Username, &encrypted, &cfg.CalendarURL, &enabled, &cfg.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return caldavConfig{}, nil
	}
	if err != nil {
		return caldavConfig{}, err
	}
	cfg.HasPassword = encrypted != ""
	cfg.Enabled = enabled != 0
	return cfg, nil
}

// caldavCredentials loads the decrypted CalDAV login. The password only
// exists in memory while opening the connection.
func loadCalDAVSecrets(ctx context.Context, db *sql.DB, userID int64, masterKey []byte) (caldavConfig, string, error) {
	cfg, err := getCalDAVConfig(ctx, db, userID)
	if err != nil {
		return caldavConfig{}, "", err
	}
	var encrypted string
	err = db.QueryRowContext(ctx,
		`SELECT encrypted_password FROM plugin_calendar_invites_caldav WHERE user_id = ?`,
		userID).Scan(&encrypted)
	if err != nil {
		return caldavConfig{}, "", err
	}
	if encrypted == "" {
		return caldavConfig{}, "", errors.New("CalDAV password is not set")
	}
	password, err := mmcrypto.DecryptString(masterKey, encrypted)
	if err != nil {
		return caldavConfig{}, "", err
	}
	return cfg, password, nil
}

func saveCalDAVConfig(ctx context.Context, db *sql.DB, userID int64, masterKey []byte, cfg caldavConfig, password string) error {
	previous, err := getCalDAVConfig(ctx, db, userID)
	if err != nil {
		return err
	}
	if password == "" && previous.HasPassword && (cfg.ServerURL != previous.ServerURL || cfg.Username != previous.Username) {
		return errors.New("enter a password when changing the CalDAV server or username")
	}
	encrypted := ""
	if password != "" {
		var err error
		encrypted, err = mmcrypto.EncryptString(masterKey, password)
		if err != nil {
			return err
		}
	}
	now := nowUnix()
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	if encrypted != "" {
		_, err := db.ExecContext(ctx, `
			INSERT INTO plugin_calendar_invites_caldav
				(user_id, server_url, username, encrypted_password, calendar_url, enabled, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET
				server_url = excluded.server_url,
				username = excluded.username,
				encrypted_password = excluded.encrypted_password,
				calendar_url = excluded.calendar_url,
				enabled = excluded.enabled,
				updated_at = excluded.updated_at`,
			userID, cfg.ServerURL, cfg.Username, encrypted, cfg.CalendarURL, enabled, now, now)
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO plugin_calendar_invites_caldav
			(user_id, server_url, username, encrypted_password, calendar_url, enabled, last_error, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, '', ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			server_url = excluded.server_url,
			username = excluded.username,
			calendar_url = excluded.calendar_url,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at`,
		userID, cfg.ServerURL, cfg.Username, cfg.CalendarURL, enabled, now, now)
	return err
}

func setCalDAVError(ctx context.Context, db *sql.DB, userID int64, lastErr string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE plugin_calendar_invites_caldav SET last_error = ?, updated_at = ? WHERE user_id = ?`,
		lastErr, nowUnix(), userID)
	return err
}
