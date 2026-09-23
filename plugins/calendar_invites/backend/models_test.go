package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func testInviteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureCalInvitesSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func fixtureEvent() icsEvent {
	return icsEvent{
		Method:        "REQUEST",
		UID:           "seq-test@example.com",
		Sequence:      1,
		Summary:       "Sequence test",
		DTStart:       "20260930T140000Z",
		DTEnd:         "20260930T150000Z",
		OrganizerAddr: "boss@example.com",
		OrganizerName: "Boss",
		Attendees: []icsAttendee{
			{Address: "christian@example.com", Name: "Christian"},
		},
	}
}

func TestUpsertInviteSequenceFlow(t *testing.T) {
	ctx := context.Background()
	db := testInviteDB(t)
	userID := int64(7)

	// Insert a fresh invite.
	if err := upsertInvite(ctx, db, userID, 42, fixtureEvent(), icsAttendee{Address: "christian@example.com", Name: "Christian"}); err != nil {
		t.Fatal(err)
	}
	invite, err := getInviteByUID(ctx, db, userID, "seq-test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if invite.Sequence != 1 || invite.Status != inviteStatusPending {
		t.Errorf("inserted invite = seq %d status %q", invite.Sequence, invite.Status)
	}
	if invite.MessageID != 42 {
		t.Errorf("message_id = %d, want 42", invite.MessageID)
	}
	if invite.AttendeeAddr != "christian@example.com" {
		t.Errorf("attendee = %q", invite.AttendeeAddr)
	}

	// Mark it answered, then receive a newer SEQUENCE: status must reset.
	if err := setInviteStatus(ctx, db, userID, invite.ID, inviteStatusAccepted); err != nil {
		t.Fatal(err)
	}
	newer := fixtureEvent()
	newer.Sequence = 2
	newer.Summary = "Sequence test (updated)"
	if err := upsertInvite(ctx, db, userID, 43, newer, icsAttendee{}); err != nil {
		t.Fatal(err)
	}
	updated, err := getInviteByUID(ctx, db, userID, "seq-test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Sequence != 2 {
		t.Errorf("sequence = %d, want 2", updated.Sequence)
	}
	if updated.Status != inviteStatusPending {
		t.Errorf("status = %q, want pending after newer sequence", updated.Status)
	}
	if updated.MessageID != 43 {
		t.Errorf("message_id = %d, want 43", updated.MessageID)
	}

	// An older SEQUENCE must not overwrite the newer one.
	older := fixtureEvent()
	older.Sequence = 1
	older.Summary = "Stale version"
	if err := upsertInvite(ctx, db, userID, 44, older, icsAttendee{}); err != nil {
		t.Fatal(err)
	}
	kept, err := getInviteByUID(ctx, db, userID, "seq-test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Sequence != 2 || kept.Summary != "Sequence test (updated)" {
		t.Errorf("stale upsert overwrote: seq %d summary %q", kept.Sequence, kept.Summary)
	}

	// Listing returns the row; status round-trips.
	invites, err := listInvites(ctx, db, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 1 {
		t.Fatalf("listInvites = %d rows, want 1", len(invites))
	}
	if invites[0].OrganizerAddr != "boss@example.com" {
		t.Errorf("organizer = %q", invites[0].OrganizerAddr)
	}
}

func TestInviteRawICSRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testInviteDB(t)
	ev := fixtureEvent()
	if err := upsertInvite(ctx, db, 1, 0, ev, icsAttendee{}); err != nil {
		t.Fatal(err)
	}
	invite, err := getInviteByUID(ctx, db, 1, ev.UID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := inviteRawICS(ctx, db, 1, invite.ID)
	if err != nil {
		t.Fatal(err)
	}
	events := parseICSEvents(raw)
	if len(events) != 1 || events[0].UID != ev.UID || events[0].Sequence != ev.Sequence {
		t.Errorf("raw ICS round trip failed: %+v", events)
	}
}

func TestCalDAVConfigEncryptedRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testInviteDB(t)
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes, as mmcrypto requires
	cfg := caldavConfig{
		ServerURL:   "https://cal.example.com",
		Username:    "christian",
		CalendarURL: "https://cal.example.com/cal/1/",
		Enabled:     true,
	}
	if err := saveCalDAVConfig(ctx, db, 1, key, cfg, "s3cret-password"); err != nil {
		t.Fatal(err)
	}
	got, err := getCalDAVConfig(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerURL != cfg.ServerURL || got.Username != cfg.Username || !got.Enabled {
		t.Errorf("config mismatch: %+v", got)
	}
	if got.HasPassword != true {
		t.Errorf("HasPassword = false after saving")
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q", got.LastError)
	}
	stored, password, err := loadCalDAVSecrets(ctx, db, 1, key)
	if err != nil {
		t.Fatal(err)
	}
	if password != "s3cret-password" {
		t.Errorf("decrypted password = %q", password)
	}
	if stored.ServerURL != cfg.ServerURL {
		t.Errorf("stored server = %q", stored.ServerURL)
	}

	// Blank password keeps the stored one.
	updatedCfg := cfg
	updatedCfg.Enabled = false
	if err := saveCalDAVConfig(ctx, db, 1, key, updatedCfg, ""); err != nil {
		t.Fatal(err)
	}
	_, password, err = loadCalDAVSecrets(ctx, db, 1, key)
	if err != nil {
		t.Fatal(err)
	}
	if password != "s3cret-password" {
		t.Errorf("password not preserved: %q", password)
	}

	// A wrong master key cannot decrypt.
	if _, _, err := loadCalDAVSecrets(ctx, db, 1, []byte("wrong-key")); err == nil {
		t.Errorf("decryption with wrong key succeeded")
	}

	// Errors persist for the settings page.
	if err := setCalDAVError(ctx, db, 1, "boom"); err != nil {
		t.Fatal(err)
	}
	got, err = getCalDAVConfig(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError != "boom" {
		t.Errorf("LastError = %q", got.LastError)
	}
}

func TestInviteCreatedAtIsUTC(t *testing.T) {
	ctx := context.Background()
	db := testInviteDB(t)
	before := time.Now().UTC().Add(-time.Minute)
	if err := upsertInvite(ctx, db, 1, 0, fixtureEvent(), icsAttendee{}); err != nil {
		t.Fatal(err)
	}
	invite, err := getInviteByUID(ctx, db, 1, "seq-test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if invite.CreatedAt < before.Unix() {
		t.Errorf("CreatedAt %d is too old", invite.CreatedAt)
	}
}
