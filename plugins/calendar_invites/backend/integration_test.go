package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rolltop/backend/store"
	_ "rolltop/plugins/catalog"
)

const detailedICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:America/Denver\r\nBEGIN:STANDARD\r\nDTSTART:19701101T020000\r\nTZOFFSETFROM:-0600\r\nTZOFFSETTO:-0700\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:detailed\r\nSEQUENCE:2\r\nDTSTART;TZID=America/Denver:20261007T090000\r\nDTEND;TZID=America/Denver:20261007T100000\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\nLOCATION:Office\r\n" +
	"ORGANIZER;CN=\"Boss; morning: meetings\":mailto:boss@example.test\r\nATTENDEE:mailto:reader@example.test\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Reminder\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestStoredInvitationPreservesCalendarData(t *testing.T) {
	ctx := context.Background()
	db := testInviteDB(t)
	events := parseICSEvents([]byte(detailedICS))
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].OrganizerName != "Boss; morning: meetings" {
		t.Fatal("quoted parameters were split")
	}
	if err := upsertInvite(ctx, db, 1, 0, events[0], icsAttendee{}); err != nil {
		t.Fatal(err)
	}
	invite, err := getInviteByUID(ctx, db, 1, "detailed")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := inviteRawICS(ctx, db, 1, invite.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != detailedICS {
		t.Fatalf("calendar data changed:\n%s", raw)
	}
	reply, err := buildReplyICS(events[0], events[0].Attendees[0], "ACCEPTED", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"METHOD:REPLY", "DTSTART;TZID=America/Denver:", "BEGIN:VTIMEZONE", "ORGANIZER;CN=\"Boss; morning: meetings\""} {
		if !strings.Contains(string(reply), expected) {
			t.Errorf("reply lost %s", expected)
		}
	}
}

func TestCalendarPayloadKeepsOnlySameUIDAndItsRecurrenceExceptions(t *testing.T) {
	raw := strings.Replace(detailedICS, "END:VCALENDAR", "BEGIN:VEVENT\r\nUID:other\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:detailed\r\nRECURRENCE-ID;TZID=America/Denver:20261014T090000\r\nEND:VEVENT\r\nEND:VCALENDAR", 1)
	events := parseICSEvents([]byte(raw))
	if len(events) != 3 {
		t.Fatalf("events = %d", len(events))
	}
	if strings.Contains(events[0].RawICS, "UID:other") || !strings.Contains(events[0].RawICS, "RECURRENCE-ID;TZID=") {
		t.Fatal("wrong recurrence set preserved")
	}
	reply, err := buildReplyICS(events[2], icsAttendee{Address: "reader@example.test"}, "ACCEPTED", time.Now())
	if err != nil || !strings.Contains(string(reply), "RECURRENCE-ID;TZID=America/Denver:20261014T090000") {
		t.Fatalf("instance reply: %s, %v", reply, err)
	}
	if len(parseICSEvents([]byte("BEGIN:VEVENT\r\nUID:outside\r\nEND:VEVENT\r\n"))) != 0 {
		t.Fatal("accepted VEVENT outside calendar")
	}
}

func TestCalendarFoldingPreservesUTF8AndLeadingWhitespace(t *testing.T) {
	line := "SUMMARY:" + strings.Repeat("é", 100) + "  suffix"
	folded := foldICSLine(line)
	for _, part := range strings.Split(folded, "\r\n") {
		if !utf8.ValidString(part) || len(part) > 75 {
			t.Fatal("invalid folded line")
		}
	}
	if strings.Join(unfoldICSLines(folded), "") != line {
		t.Fatal("fold round trip changed text")
	}
	if got := unfoldICSLines("SUMMARY:a\r\n  b"); got[0] != "SUMMARY:a b" {
		t.Fatal(got)
	}
}

type inviteTestHost struct {
	st      *store.Store
	enabled bool
}

func (h inviteTestHost) Store() any                                 { return h.st }
func (h inviteTestHost) MasterKey() []byte                          { return []byte("0123456789abcdef0123456789abcdef") }
func (h inviteTestHost) PluginEnabled(context.Context, string) bool { return h.enabled }

func newInviteStore(t *testing.T) (*store.Store, store.User, store.User) {
	t.Helper()
	root := t.TempDir()
	st, err := store.OpenServer(filepath.Join(root, "rolltop.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	one, err := st.CreateUser(context.Background(), "reader@example.test", "Reader", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	two, err := st.CreateUser(context.Background(), "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	return st, one, two
}

func TestInlineMIMEImportAndTenantIsolation(t *testing.T) {
	st, one, two := newInviteStore(t)
	ctx := context.Background()
	p := &calendarInvitesBackend{}
	raw := "From: boss@example.test\r\nContent-Type: multipart/alternative; boundary=parts\r\n\r\n--parts\r\nContent-Type: text/plain\r\n\r\nMeeting\r\n--parts\r\nContent-Type: text/calendar; method=REQUEST\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(detailedICS)) + "\r\n--parts--\r\n"
	if err := p.ImportIncomingMessage(ctx, inviteTestHost{st, true}, one.ID, []byte(raw), ""); err != nil {
		t.Fatal(err)
	}
	db, _ := st.UserDB(ctx, one.ID)
	invites, err := listInvites(ctx, db, one.ID)
	if err != nil || len(invites) != 1 {
		t.Fatalf("inline invitation: %v, %v", invites, err)
	}
	if _, err := getInvite(ctx, db, two.ID, invites[0].ID); !errors.Is(err, errInviteNotFound) {
		t.Fatal("cross-user read allowed")
	}
	if _, err := inviteRawICS(ctx, db, two.ID, invites[0].ID); !errors.Is(err, errInviteNotFound) {
		t.Fatal("cross-user payload read allowed")
	}
	if err := setInviteStatus(ctx, db, two.ID, invites[0].ID, inviteStatusAccepted); !errors.Is(err, errInviteNotFound) {
		t.Fatal("cross-user update allowed")
	}
	otherDB, _ := st.UserDB(ctx, two.ID)
	if rows, err := listInvites(ctx, otherDB, two.ID); err != nil || len(rows) != 0 {
		t.Fatal("invitation leaked to other database")
	}
	if err := p.ImportIncomingMessage(ctx, inviteTestHost{st, false}, two.ID, []byte(raw), ""); err != nil {
		t.Fatal(err)
	}
	if rows, _ := listInvites(ctx, otherDB, two.ID); len(rows) != 0 {
		t.Fatal("disabled plugin imported invitations")
	}
	inline := "Content-Type: text/calendar\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + strings.ReplaceAll(detailedICS, "=", "=3D")
	if parts := calendarMIMEParts([]byte(inline)); len(parts) != 1 || string(parts[0]) != detailedICS {
		t.Fatal("unnamed quoted-printable calendar missed")
	}
}

func TestRSVPOutboxRetriesAndIsolation(t *testing.T) {
	st, one, two := newInviteStore(t)
	ctx := context.Background()
	account, err := st.CreateMailAccount(ctx, store.MailAccount{UserID: one.ID, Email: one.Email, Host: "imap.example.test", Port: 993, Username: one.Email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*"})
	if err != nil {
		t.Fatal(err)
	}
	smtp, err := st.CreateSMTPAccount(ctx, store.SMTPAccount{UserID: one.ID, Label: "Mail", Host: "smtp.example.test", Port: 587, Username: one.Email, EncryptedPassword: "encrypted", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := st.GetOrCreateMailboxWithRole(ctx, one.ID, account.ID, "Sent", "sent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateMailIdentityForUser(ctx, one.ID, store.MailIdentity{Email: one.Email, SMTPAccountID: smtp.ID, IMAPAccountID: account.ID, SentMailboxID: sent.ID})
	if err != nil {
		t.Fatal(err)
	}
	db, _ := st.UserDB(ctx, one.ID)
	ev := parseICSEvents([]byte(detailedICS))[0]
	if err := upsertInvite(ctx, db, one.ID, 0, ev, ev.Attendees[0]); err != nil {
		t.Fatal(err)
	}
	invite, _ := getInviteByUID(ctx, db, one.ID, ev.UID)
	if err := sendRSVP(ctx, st, db, two.ID, invite.ID, "ACCEPTED", 2); !errors.Is(err, errInviteNotFound) {
		t.Fatalf("foreign RSVP: %v", err)
	}
	if err := sendRSVP(ctx, st, db, one.ID, invite.ID, "ACCEPTED", 1); !errors.Is(err, errStaleSequence) {
		t.Fatalf("stale RSVP: %v", err)
	}
	// Simulate a durable enqueue followed by failure to persist invite status.
	if _, err := db.Exec(`CREATE TRIGGER fail_rsvp_status BEFORE UPDATE OF status ON plugin_calendar_invites_invites BEGIN SELECT RAISE(FAIL, 'status unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := sendRSVP(ctx, st, db, one.ID, invite.ID, "ACCEPTED", 2); err == nil {
		t.Fatal("expected status failure")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_rsvp_status`); err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{"ACCEPTED", "ACCEPTED", "DECLINED", "ACCEPTED"} {
		if err := sendRSVP(ctx, st, db, one.ID, invite.ID, response, 2); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_jobs WHERE user_id = ?`, one.ID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("outbox count = %d, %v", count, err)
	}
	rows, err := db.Query(`SELECT blob_path FROM outbox_jobs WHERE user_id = ?`, one.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(path, "users/1/blobs/outbox/") {
			t.Fatal("wrong spool scope")
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(st.UserDataDir(one.ID))), path))
		if err != nil || len(calendarMIMEParts(raw)) != 1 {
			t.Fatalf("invalid outbox MIME: %v", err)
		}
	}
}

func TestCalDAVRedirectPreservesMethodAndRejectsForeignCredentials(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls++; w.WriteHeader(200) }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/target")
			w.WriteHeader(302)
		case "/foreign":
			w.Header().Set("Location", foreign.URL+"/secret")
			w.WriteHeader(307)
		default:
			if r.Method != "PROPFIND" {
				t.Error("DAV method changed on redirect")
			}
			user, pass, _ := r.BasicAuth()
			if user != "u" || pass != "secret" {
				t.Error("missing auth")
			}
			w.WriteHeader(207)
		}
	}))
	defer server.Close()
	client, err := newCalDAVClient(server.URL, caldavCredentials{"u", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.doFollowRedirect(context.Background(), "PROPFIND", server.URL+"/redirect", "body", "text/xml", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.doFollowRedirect(context.Background(), "PROPFIND", server.URL+"/foreign", "body", "text/xml", nil); err == nil {
		t.Fatal("followed foreign redirect")
	}
	if _, err := client.do(context.Background(), "PROPFIND", foreign.URL, "", "", nil); err == nil {
		t.Fatal("accepted foreign DAV href")
	}
	if foreignCalls != 0 {
		t.Fatal("credentials sent to foreign server")
	}
	if _, err := newCalDAVClient("http://127.evil.example", caldavCredentials{"u", "secret"}); err == nil {
		t.Fatal("accepted fake loopback host")
	}
	if _, err := newCalDAVClient("https://user:secret@example.test", caldavCredentials{"u", "secret"}); err == nil {
		t.Fatal("accepted URL credentials")
	}
}
