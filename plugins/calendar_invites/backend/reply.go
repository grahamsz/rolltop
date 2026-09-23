// File overview: Builds RFC 5546 METHOD:REPLY payloads answering a calendar
// invitation. The reply echoes the invitation's UID and SEQUENCE, keeps the
// ORGANIZER untouched, and carries only the answering attendee with the chosen
// PARTSTAT, so the organizer's calendar can match the reply to the event.

package main

import (
	"fmt"
	"strings"
	"time"
)

// validPartStats are the attendee responses the plugin offers.
var validPartStats = map[string]bool{
	"ACCEPTED":  true,
	"TENTATIVE": true,
	"DECLINED":  true,
}

// partstatLabel is the human verb used in the reply subject and body.
func partstatLabel(partstat string) string {
	switch strings.ToUpper(partstat) {
	case "ACCEPTED":
		return "Accepted"
	case "TENTATIVE":
		return "Tentative"
	case "DECLINED":
		return "Declined"
	default:
		return partstat
	}
}

// escapeICSText escapes text values per RFC 5545 section 3.3.11.
func escapeICSText(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"\n", `\n`,
		",", `\,`,
		";", `\;`,
	)
	return replacer.Replace(value)
}

// foldICSLine folds one content line at 75 octets per RFC 5545 section 3.1.
// The payload is ASCII by construction, so byte folding is safe.
func foldICSLine(line string) string {
	if len(line) <= 75 {
		return line
	}
	var out strings.Builder
	out.WriteString(line[:75])
	rest := line[75:]
	for len(rest) > 0 {
		out.WriteString("\r\n ")
		n := 74
		if len(rest) < n {
			n = len(rest)
		}
		out.WriteString(rest[:n])
		rest = rest[n:]
	}
	return out.String()
}

// formatICSDate formats a time as a UTC DATE-TIME value.
func formatICSDate(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// buildReplyICS renders the METHOD:REPLY calendar object answering invite.
// DTSTART/DTEND/SUMMARY are echoed for organizer-side context; RECURRENCE-ID
// is intentionally not echoed because this prototype answers the whole event.
func buildReplyICS(invite icsEvent, attendee icsAttendee, partstat string, now time.Time) ([]byte, error) {
	partstat = strings.ToUpper(strings.TrimSpace(partstat))
	if !validPartStats[partstat] {
		return nil, fmt.Errorf("invalid PARTSTAT %q", partstat)
	}
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Rolltop//Calendar Invites//EN",
		"METHOD:REPLY",
		"BEGIN:VEVENT",
		"UID:" + invite.UID,
		"DTSTAMP:" + formatICSDate(now),
		fmt.Sprintf("SEQUENCE:%d", invite.Sequence),
	}
	if invite.DTStart != "" {
		lines = append(lines, "DTSTART:"+invite.DTStart)
	}
	if invite.DTEnd != "" {
		lines = append(lines, "DTEND:"+invite.DTEnd)
	}
	if invite.Summary != "" {
		lines = append(lines, "SUMMARY:"+escapeICSText(invite.Summary))
	}
	orgLine := "ORGANIZER"
	if invite.OrganizerName != "" {
		orgLine += ";CN=" + escapeICSText(invite.OrganizerName)
	}
	lines = append(lines, orgLine+":mailto:"+invite.OrganizerAddr)
	attLine := "ATTENDEE;PARTSTAT=" + partstat
	if attendee.Name != "" {
		attLine += ";CN=" + escapeICSText(attendee.Name)
	}
	lines = append(lines, attLine+":mailto:"+attendee.Address)
	lines = append(lines, "END:VEVENT", "END:VCALENDAR", "")
	folded := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			folded = append(folded, "")
			continue
		}
		folded = append(folded, foldICSLine(line))
	}
	return []byte(strings.Join(folded, "\r\n")), nil
}
