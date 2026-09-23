package main

import (
	"strings"
	"testing"
)

const testInviteICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Example Corp//CalDAV//EN\r\n" +
	"METHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:invite-123@example.com\r\n" +
	"DTSTAMP:20260923T120000Z\r\n" +
	"SEQUENCE:2\r\n" +
	"DTSTART:20260930T140000Z\r\n" +
	"DTEND:20260930T150000Z\r\n" +
	"SUMMARY:Team sync, with comma\\; and semicolon\r\n" +
	"ORGANIZER;CN=Organizer Person:mailto:boss@example.com\r\n" +
	"ATTENDEE;CN=Christian McHugh;PARTSTAT=NEEDS-ACTION;ROLE=REQ-PARTICIPANT:mailto:christian@example.com\r\n" +
	"ATTENDEE;CN=Second Person:mailto:second@example.com\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestParseInviteBasics(t *testing.T) {
	events := parseICSEvents([]byte(testInviteICS))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Method != "REQUEST" {
		t.Errorf("method = %q, want REQUEST", ev.Method)
	}
	if ev.UID != "invite-123@example.com" {
		t.Errorf("uid = %q", ev.UID)
	}
	if ev.Sequence != 2 {
		t.Errorf("sequence = %d, want 2", ev.Sequence)
	}
	if ev.DTStart != "20260930T140000Z" || ev.DTEnd != "20260930T150000Z" {
		t.Errorf("dtstart/dtend = %q/%q", ev.DTStart, ev.DTEnd)
	}
	if ev.Summary != "Team sync, with comma; and semicolon" {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.OrganizerAddr != "boss@example.com" || ev.OrganizerName != "Organizer Person" {
		t.Errorf("organizer = %q %q", ev.OrganizerName, ev.OrganizerAddr)
	}
	if len(ev.Attendees) != 2 {
		t.Fatalf("attendees = %d, want 2", len(ev.Attendees))
	}
	first := ev.Attendees[0]
	if first.Address != "christian@example.com" || first.Name != "Christian McHugh" {
		t.Errorf("attendee[0] = %q %q", first.Name, first.Address)
	}
	if first.PartStat != "NEEDS-ACTION" || first.Role != "REQ-PARTICIPANT" {
		t.Errorf("attendee[0] partstat/role = %q/%q", first.PartStat, first.Role)
	}
}

func TestParseInviteFoldedLines(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:folded-1\r\n" +
		"SUMMARY:This is a very long summary that has been folded across \r\n" +
		" multiple content lines per RFC 5545\r\n" +
		"ORGANIZER;CN=\"Doe, Jane\":mailto:jane@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	events := parseICSEvents([]byte(raw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	want := "This is a very long summary that has been folded across multiple content lines per RFC 5545"
	if ev.Summary != want {
		t.Errorf("summary = %q, want %q", ev.Summary, want)
	}
	if ev.OrganizerName != "Doe, Jane" {
		t.Errorf("organizer CN with comma = %q", ev.OrganizerName)
	}
}

func TestParseInviteQuotedColonParam(t *testing.T) {
	// A colon inside a quoted CN must not split the property head from the value.
	raw := "BEGIN:VCALENDAR\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:colon-1\r\n" +
		"ATTENDEE;CN=\"Shift: morning\":mailto:worker@example.com\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	events := parseICSEvents([]byte(raw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	att := events[0].Attendees[0]
	if att.Name != "Shift: morning" {
		t.Errorf("attendee CN = %q", att.Name)
	}
	if att.Address != "worker@example.com" {
		t.Errorf("attendee address = %q", att.Address)
	}
}

func TestParseInviteSkipsNonRequest(t *testing.T) {
	raw := strings.Replace(testInviteICS, "METHOD:REQUEST", "METHOD:CANCEL", 1)
	events := parseICSEvents([]byte(raw))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if isInviteMethod(events[0].Method) {
		t.Errorf("CANCEL must not be treated as an actionable invite")
	}
}

func TestParseInviteSkipsMissingUID(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nSUMMARY:No UID\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if events := parseICSEvents([]byte(raw)); len(events) != 0 {
		t.Errorf("expected 0 events for UID-less VEVENT, got %d", len(events))
	}
}

func TestParseInviteMultipleEvents(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:one\r\nSUMMARY:One\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:two\r\nSUMMARY:Two\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	events := parseICSEvents([]byte(raw))
	if len(events) != 2 || events[0].UID != "one" || events[1].UID != "two" {
		t.Errorf("expected two events one/two, got %+v", events)
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := map[string]string{
		"mailto:User@Example.COM":     "user@example.com",
		"user@example.com":            "user@example.com",
		"  MAILTO:User@Example.COM  ": "user@example.com",
	}
	for in, want := range cases {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
