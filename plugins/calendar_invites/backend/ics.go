// File overview: Self-contained iCalendar (RFC 5545) parser for calendar
// invites. It unfolds content lines, parses properties with parameters, and
// extracts the VEVENT fields the plugin needs (METHOD, UID, SEQUENCE,
// DTSTAMP, DTSTART, DTEND, SUMMARY, ORGANIZER, ATTENDEE). Only VEVENTs inside
// a VCALENDAR are returned; anything else in the payload is ignored.

package main

import (
	"strconv"
	"strings"
)

// icsProperty is one unfolded iCalendar content line.
type icsProperty struct {
	Name   string
	Params map[string]string
	Value  string
}

// icsAttendee is one ATTENDEE property of a VEVENT.
type icsAttendee struct {
	Address  string
	Name     string
	PartStat string
	Role     string
}

// icsEvent is a single VEVENT worth tracking: an invitation the user can RSVP
// to. Method comes from the enclosing VCALENDAR.
type icsEvent struct {
	Method        string
	UID           string
	Sequence      int
	DTStamp       string
	DTStart       string
	DTEnd         string
	Summary       string
	OrganizerAddr string
	OrganizerName string
	Attendees     []icsAttendee
}

// unfoldICSLines joins folded content lines per RFC 5545 section 3.1: a line
// starting with a space or tab continues the previous line.
func unfoldICSLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	raw := strings.Split(value, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(out) > 0 {
				out[len(out)-1] += strings.TrimLeft(line, " \t")
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

// splitICSProperty splits a content line into head (name + params) and value
// at the first colon that is not inside a quoted parameter value.
func splitICSProperty(raw string) (string, string, bool) {
	inQuotes := false
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			inQuotes = !inQuotes
		case ':':
			if !inQuotes {
				return raw[:i], raw[i+1:], true
			}
		}
	}
	return "", "", false
}

// parseICSProperty parses one unfolded content line into name, params, value.
// Parameter names are uppercased; a bare token without "=" is treated as a
// TYPE value, mirroring common vCard/ICS parser behavior.
func parseICSProperty(raw string) (icsProperty, bool) {
	head, value, ok := splitICSProperty(raw)
	if !ok || strings.TrimSpace(head) == "" {
		return icsProperty{}, false
	}
	parts := strings.Split(head, ";")
	prop := icsProperty{
		Name:   strings.ToUpper(strings.TrimSpace(parts[0])),
		Params: map[string]string{},
		Value:  value,
	}
	for _, param := range parts[1:] {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		key, val, found := strings.Cut(param, "=")
		if !found {
			key, val = "TYPE", param
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		val = strings.Trim(strings.TrimSpace(val), `"`)
		if key == "" || val == "" {
			continue
		}
		prop.Params[key] = val
	}
	return prop, true
}

// unescapeICS reverses RFC 5545 text escaping.
func unescapeICS(value string) string {
	replacer := strings.NewReplacer(
		`\\`, "\u0000",
		`\n`, "\n",
		`\N`, "\n",
		`\,`, ",",
		`\;`, ";",
	)
	out := replacer.Replace(value)
	return strings.ReplaceAll(out, "\u0000", `\`)
}

// stripMailto removes a leading "mailto:" scheme from a calendar address.
func stripMailto(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "mailto:") {
		return strings.TrimSpace(value[len("mailto:"):])
	}
	return value
}

// normalizeAddr lowercases and trims a calendar address for comparison.
func normalizeAddr(value string) string {
	return strings.ToLower(stripMailto(value))
}

// parseICSEvents extracts VEVENTs from raw iCalendar data. Events are only
// returned with their enclosing VCALENDAR's METHOD; callers filter on
// METHOD:REQUEST themselves.
func parseICSEvents(data []byte) []icsEvent {
	var out []icsEvent
	var (
		method  string
		inEvent bool
		props   []icsProperty
	)
	flush := func() {
		if !inEvent {
			return
		}
		if ev, ok := eventFromProps(method, props); ok {
			out = append(out, ev)
		}
		inEvent = false
		props = nil
	}
	for _, raw := range unfoldICSLines(string(data)) {
		prop, ok := parseICSProperty(raw)
		if !ok {
			continue
		}
		switch prop.Name {
		case "BEGIN":
			switch strings.ToUpper(strings.TrimSpace(prop.Value)) {
			case "VCALENDAR":
				method = ""
			case "VEVENT":
				inEvent = true
				props = nil
			}
		case "END":
			switch strings.ToUpper(strings.TrimSpace(prop.Value)) {
			case "VEVENT":
				flush()
			case "VCALENDAR":
				flush()
				method = ""
			}
		case "METHOD":
			if !inEvent {
				method = strings.ToUpper(strings.TrimSpace(prop.Value))
			}
		default:
			if inEvent {
				props = append(props, prop)
			}
		}
	}
	flush()
	return out
}

// eventFromProps builds an icsEvent from one VEVENT's properties. Events
// without a UID are skipped: the invite table and the RSVP flow key on UID.
func eventFromProps(method string, props []icsProperty) (icsEvent, bool) {
	ev := icsEvent{Method: method}
	for _, prop := range props {
		value := strings.TrimSpace(prop.Value)
		switch prop.Name {
		case "UID":
			ev.UID = value
		case "SEQUENCE":
			if n, err := strconv.Atoi(value); err == nil {
				ev.Sequence = n
			}
		case "DTSTAMP":
			ev.DTStamp = value
		case "DTSTART":
			ev.DTStart = value
		case "DTEND":
			ev.DTEnd = value
		case "SUMMARY":
			ev.Summary = unescapeICS(value)
		case "ORGANIZER":
			ev.OrganizerAddr = stripMailto(value)
			ev.OrganizerName = unescapeICS(prop.Params["CN"])
		case "ATTENDEE":
			ev.Attendees = append(ev.Attendees, icsAttendee{
				Address:  stripMailto(value),
				Name:     unescapeICS(prop.Params["CN"]),
				PartStat: strings.ToUpper(strings.TrimSpace(prop.Params["PARTSTAT"])),
				Role:     strings.ToUpper(strings.TrimSpace(prop.Params["ROLE"])),
			})
		}
	}
	if ev.UID == "" {
		return icsEvent{}, false
	}
	return ev, true
}

// isInviteMethod reports whether METHOD carries an invitation the user can
// answer. Only REQUEST is actionable; CANCEL/PUBLISH/REPLY are informational.
func isInviteMethod(method string) bool {
	return strings.EqualFold(strings.TrimSpace(method), "REQUEST")
}
