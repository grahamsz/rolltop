// File overview: Builds RFC 5546 METHOD:REPLY payloads answering a calendar
// invitation. The reply echoes the invitation's UID and SEQUENCE, keeps the
// ORGANIZER untouched, and carries only the answering attendee with the chosen
// PARTSTAT, so the organizer's calendar can match the reply to the event.

package main

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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
		"\r", "",
		"\n", `\n`,
		",", `\,`,
		";", `\;`,
	)
	return replacer.Replace(value)
}

// foldICSLine folds one content line at 75 octets per RFC 5545 section 3.1.
// UTF-8 characters stay intact at fold boundaries.
func foldICSLine(line string) string {
	var out strings.Builder
	limit := 75
	for len(line) > limit {
		n := limit
		for n > 0 && !utf8.RuneStart(line[n]) {
			n--
		}
		if n == 0 {
			// Malformed input must still make progress instead of looping.
			n = limit
		}
		out.WriteString(line[:n])
		out.WriteString("\r\n ")
		line = line[n:]
		limit = 74
	}
	out.WriteString(line)
	return out.String()
}

func icsParameter(value string) string {
	value = strings.NewReplacer("^", "^^", "\r", "", "\n", "^n", "\"", "^'").Replace(value)
	if strings.ContainsAny(value, ":;, ") {
		return "\"" + value + "\""
	}
	return value
}

// formatICSDate formats a time as a UTC DATE-TIME value.
func formatICSDate(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// buildReplyICS renders the METHOD:REPLY calendar object answering invite.
// Preserve date parameters, timezone definitions, and recurrence-instance IDs.
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
	original := map[string]string{}
	for _, prop := range invite.Properties {
		original[prop.Name] = prop.Raw
	}
	if len(invite.Properties) > 0 {
		for _, name := range []string{"DTSTART", "DTEND", "DURATION", "RECURRENCE-ID"} {
			if raw := original[name]; raw != "" {
				lines = append(lines, raw)
			}
		}
	} else if invite.DTStart != "" {
		lines = append(lines, "DTSTART:"+invite.DTStart)
	}
	if len(invite.Properties) == 0 && invite.DTEnd != "" {
		lines = append(lines, "DTEND:"+invite.DTEnd)
	}
	if invite.Summary != "" {
		lines = append(lines, "SUMMARY:"+escapeICSText(invite.Summary))
	}
	orgLine := "ORGANIZER"
	if invite.OrganizerName != "" {
		orgLine += ";CN=" + icsParameter(invite.OrganizerName)
	}
	if original["ORGANIZER"] != "" {
		lines = append(lines, original["ORGANIZER"])
	} else {
		lines = append(lines, orgLine+":mailto:"+invite.OrganizerAddr)
	}
	attLine := "ATTENDEE;PARTSTAT=" + partstat
	if attendee.Name != "" {
		attLine += ";CN=" + icsParameter(attendee.Name)
	}
	lines = append(lines, attLine+":mailto:"+attendee.Address)
	lines = append(lines, "END:VEVENT")
	for _, calendar := range parseICSCalendars([]byte(invite.RawICS)) {
		for _, component := range calendar.children {
			if component.name == "VTIMEZONE" {
				lines = append(lines, strings.TrimSuffix(component.render(), "\r\n"))
			}
		}
	}
	lines = append(lines, "END:VCALENDAR", "")
	folded := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" || strings.Contains(line, "\r\n") {
			folded = append(folded, line)
			continue
		}
		folded = append(folded, foldICSLine(line))
	}
	return []byte(strings.Join(folded, "\r\n")), nil
}
