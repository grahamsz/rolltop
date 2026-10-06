package main

import (
	"strings"
	"testing"
	"time"
)

func replyTestEvent() icsEvent {
	events := parseICSEvents([]byte(testInviteICS))
	if len(events) != 1 {
		panic("test fixture did not parse")
	}
	return events[0]
}

func TestBuildReplyAccept(t *testing.T) {
	ev := replyTestEvent()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	ics, err := buildReplyICS(ev, ev.Attendees[0], "ACCEPTED", now)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(unfoldICSLines(string(ics)), "\r\n")
	for _, want := range []string{
		"METHOD:REPLY",
		"UID:invite-123@example.com",
		"SEQUENCE:2",
		"ORGANIZER;CN=Organizer Person:mailto:boss@example.com",
		"PARTSTAT=ACCEPTED",
		"mailto:christian@example.com",
		"DTSTAMP:20260923T120000Z",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("reply missing %q\n%s", want, text)
		}
	}
	// Only the answering attendee appears; the second attendee must not.
	if strings.Contains(text, "second@example.com") {
		t.Errorf("reply must not include other attendees\n%s", text)
	}
	if n := strings.Count(text, "ATTENDEE"); n != 1 {
		t.Errorf("reply has %d ATTENDEE lines, want 1", n)
	}
}

func TestBuildReplyDeclinedAndTentative(t *testing.T) {
	ev := replyTestEvent()
	now := time.Now().UTC()
	for _, partstat := range []string{"DECLINED", "TENTATIVE"} {
		ics, err := buildReplyICS(ev, ev.Attendees[0], partstat, now)
		if err != nil {
			t.Fatal(err)
		}
		text := string(ics)
		if !strings.Contains(text, "PARTSTAT="+partstat) {
			t.Errorf("reply missing PARTSTAT=%s\n%s", partstat, text)
		}
	}
}

func TestBuildReplyRejectsBadPartStat(t *testing.T) {
	ev := replyTestEvent()
	if _, err := buildReplyICS(ev, ev.Attendees[0], "MAYBE", time.Now().UTC()); err == nil {
		t.Errorf("buildReplyICS accepted invalid PARTSTAT")
	}
}

func TestCheckRSVPSequence(t *testing.T) {
	if err := checkRSVPSequence(2, 2); err != nil {
		t.Errorf("matching sequence rejected: %v", err)
	}
	if err := checkRSVPSequence(2, -1); err != nil {
		t.Errorf("negative wantSequence rejected: %v", err)
	}
	if err := checkRSVPSequence(3, 2); err == nil {
		t.Errorf("stale sequence accepted")
	} else if !strings.Contains(err.Error(), "newer version") {
		t.Errorf("unexpected stale error: %v", err)
	}
}

func TestRSVPSubjectAndBody(t *testing.T) {
	if got := rsvpSubject("ACCEPTED", "Team sync"); got != "Accepted: Team sync" {
		t.Errorf("subject = %q", got)
	}
	if got := rsvpSubject("DECLINED", ""); got != "Declined" {
		t.Errorf("empty-summary subject = %q", got)
	}
	body := rsvpBodyText("Christian McHugh", "christian@example.com", "TENTATIVE", "Team sync")
	for _, want := range []string{"Christian McHugh", "christian@example.com", "tentatively accepted", "Team sync"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
	// Missing display name falls back to the email address.
	body = rsvpBodyText("", "christian@example.com", "ACCEPTED", "")
	if !strings.HasPrefix(body, "christian@example.com (christian@example.com) has accepted") {
		t.Errorf("fallback body = %q", body)
	}
}

func TestFoldICSLineMakesProgressWithInvalidUTF8(t *testing.T) {
	raw := strings.Repeat(string([]byte{0x80}), 160)
	if got := strings.Join(unfoldICSLines(foldICSLine(raw)), ""); got != raw {
		t.Fatal("folding malformed text lost bytes")
	}
}
