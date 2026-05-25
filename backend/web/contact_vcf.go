package web

import (
	"bytes"
	"fmt"
	"strings"

	"mailmirror/backend/store"
)

type vcardLine struct {
	Name   string
	Params map[string][]string
	Value  string
}

func parseVCards(data []byte) ([]store.Contact, error) {
	lines := unfoldVCardLines(string(data))
	var contacts []store.Contact
	var card []vcardLine
	inCard := false
	for _, raw := range lines {
		line, ok := parseVCardLine(raw)
		if !ok {
			continue
		}
		switch line.Name {
		case "BEGIN":
			if strings.EqualFold(strings.TrimSpace(line.Value), "VCARD") {
				inCard = true
				card = nil
			}
		case "END":
			if inCard && strings.EqualFold(strings.TrimSpace(line.Value), "VCARD") {
				contacts = append(contacts, contactFromVCard(card))
				inCard = false
				card = nil
			}
		default:
			if inCard {
				card = append(card, line)
			}
		}
	}
	return contacts, nil
}

func unfoldVCardLines(value string) []string {
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

func parseVCardLine(raw string) (vcardLine, bool) {
	raw = strings.TrimRight(raw, "\n")
	if strings.TrimSpace(raw) == "" {
		return vcardLine{}, false
	}
	idx := strings.Index(raw, ":")
	if idx < 0 {
		return vcardLine{}, false
	}
	head := raw[:idx]
	value := raw[idx+1:]
	parts := strings.Split(head, ";")
	line := vcardLine{Name: strings.ToUpper(strings.TrimSpace(parts[0])), Params: map[string][]string{}, Value: value}
	for _, param := range parts[1:] {
		if strings.TrimSpace(param) == "" {
			continue
		}
		key, val, ok := strings.Cut(param, "=")
		if !ok {
			key, val = "TYPE", param
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		for _, item := range strings.Split(val, ",") {
			item = strings.Trim(strings.TrimSpace(item), `"`)
			if item != "" {
				line.Params[key] = append(line.Params[key], item)
			}
		}
	}
	return line, true
}

func contactFromVCard(lines []vcardLine) store.Contact {
	var c store.Contact
	for _, line := range lines {
		value := unescapeVCard(line.Value)
		switch line.Name {
		case "FN":
			c.DisplayName = strings.TrimSpace(value)
		case "N":
			parts := splitVCardList(line.Value, 5)
			c.FamilyName = part(parts, 0)
			c.GivenName = part(parts, 1)
			c.AdditionalName = part(parts, 2)
			c.NamePrefix = part(parts, 3)
			c.NameSuffix = part(parts, 4)
		case "NICKNAME":
			c.Nickname = strings.TrimSpace(value)
		case "ORG":
			parts := splitVCardList(line.Value, 2)
			c.Organization = part(parts, 0)
			c.Department = part(parts, 1)
		case "TITLE":
			c.JobTitle = strings.TrimSpace(value)
		case "BDAY":
			c.Birthday = strings.TrimSpace(value)
		case "NOTE":
			c.Notes = strings.TrimSpace(value)
		case "CATEGORIES":
			c.Categories = strings.TrimSpace(value)
		case "EMAIL":
			if strings.TrimSpace(value) != "" {
				c.Emails = append(c.Emails, store.ContactEmail{Label: vCardLabel(line), Email: strings.TrimSpace(value), IsPrimary: vCardPref(line)})
			}
		case "TEL":
			if strings.TrimSpace(value) != "" {
				c.Phones = append(c.Phones, store.ContactPhone{Label: vCardLabel(line), Number: strings.TrimSpace(value), IsPrimary: vCardPref(line)})
			}
		case "ADR":
			parts := splitVCardList(line.Value, 7)
			addr := store.ContactAddress{
				Label:      vCardLabel(line),
				Street:     part(parts, 2),
				Locality:   part(parts, 3),
				Region:     part(parts, 4),
				PostalCode: part(parts, 5),
				Country:    part(parts, 6),
				IsPrimary:  vCardPref(line),
			}
			if strings.TrimSpace(addr.Street+addr.Locality+addr.Region+addr.PostalCode+addr.Country) != "" {
				c.Addresses = append(c.Addresses, addr)
			}
		case "URL":
			if strings.TrimSpace(value) != "" {
				c.URLs = append(c.URLs, store.ContactURL{Label: vCardLabel(line), URL: strings.TrimSpace(value), IsPrimary: vCardPref(line)})
			}
		}
	}
	if c.DisplayName == "" {
		c.DisplayName = strings.TrimSpace(strings.Join(strings.Fields(c.GivenName+" "+c.FamilyName), " "))
	}
	if c.DisplayName == "" && len(c.Emails) > 0 {
		c.DisplayName = c.Emails[0].Email
	}
	return c
}

func writeVCards(contacts []store.Contact) []byte {
	var b bytes.Buffer
	for _, c := range contacts {
		b.WriteString("BEGIN:VCARD\r\n")
		b.WriteString("VERSION:3.0\r\n")
		writeVCardProp(&b, "FN", firstNonEmpty(c.DisplayName, strings.Join(strings.Fields(c.GivenName+" "+c.FamilyName), " "), c.Organization, primaryEmail(c)))
		writeVCardRaw(&b, "N", strings.Join([]string{
			escapeVCard(c.FamilyName),
			escapeVCard(c.GivenName),
			escapeVCard(c.AdditionalName),
			escapeVCard(c.NamePrefix),
			escapeVCard(c.NameSuffix),
		}, ";"))
		if c.Nickname != "" {
			writeVCardProp(&b, "NICKNAME", c.Nickname)
		}
		if c.Organization != "" || c.Department != "" {
			writeVCardRaw(&b, "ORG", strings.Join([]string{escapeVCard(c.Organization), escapeVCard(c.Department)}, ";"))
		}
		if c.JobTitle != "" {
			writeVCardProp(&b, "TITLE", c.JobTitle)
		}
		if c.Birthday != "" {
			writeVCardProp(&b, "BDAY", c.Birthday)
		}
		if c.Notes != "" {
			writeVCardProp(&b, "NOTE", c.Notes)
		}
		if c.Categories != "" {
			writeVCardProp(&b, "CATEGORIES", c.Categories)
		}
		for _, email := range c.Emails {
			writeTypedVCardProp(&b, "EMAIL", email.Label, email.IsPrimary, email.Email)
		}
		for _, phone := range c.Phones {
			writeTypedVCardProp(&b, "TEL", phone.Label, phone.IsPrimary, phone.Number)
		}
		for _, addr := range c.Addresses {
			value := strings.Join([]string{"", "", escapeVCard(addr.Street), escapeVCard(addr.Locality), escapeVCard(addr.Region), escapeVCard(addr.PostalCode), escapeVCard(addr.Country)}, ";")
			writeTypedVCardRaw(&b, "ADR", addr.Label, addr.IsPrimary, value)
		}
		for _, u := range c.URLs {
			writeTypedVCardProp(&b, "URL", u.Label, u.IsPrimary, u.URL)
		}
		b.WriteString("END:VCARD\r\n")
	}
	return b.Bytes()
}

func splitVCardList(value string, min int) []string {
	var parts []string
	var b strings.Builder
	escaped := false
	for _, r := range value {
		if escaped {
			switch r {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == ';' {
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	parts = append(parts, strings.TrimSpace(b.String()))
	for len(parts) < min {
		parts = append(parts, "")
	}
	return parts
}

func unescapeVCard(value string) string {
	var b strings.Builder
	escaped := false
	for _, r := range value {
		if escaped {
			switch r {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func escapeVCard(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	return value
}

func writeVCardProp(b *bytes.Buffer, name, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	writeVCardRaw(b, name, escapeVCard(value))
}

func writeTypedVCardProp(b *bytes.Buffer, name, label string, preferred bool, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	writeTypedVCardRaw(b, name, label, preferred, escapeVCard(value))
}

func writeTypedVCardRaw(b *bytes.Buffer, name, label string, preferred bool, escapedValue string) {
	var params []string
	if label = strings.TrimSpace(label); label != "" {
		params = append(params, "TYPE="+escapeVCardType(label))
	}
	if preferred {
		params = append(params, "PREF=1")
	}
	if len(params) > 0 {
		name += ";" + strings.Join(params, ";")
	}
	writeVCardRaw(b, name, escapedValue)
}

func writeVCardRaw(b *bytes.Buffer, name, escapedValue string) {
	line := fmt.Sprintf("%s:%s", name, escapedValue)
	for len(line) > 73 {
		b.WriteString(line[:73])
		b.WriteString("\r\n ")
		line = line[73:]
	}
	b.WriteString(line)
	b.WriteString("\r\n")
}

func escapeVCardType(value string) string {
	value = strings.ReplaceAll(value, ";", "")
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, ",", "")
	return strings.TrimSpace(value)
}

func vCardLabel(line vcardLine) string {
	types := line.Params["TYPE"]
	if len(types) == 0 {
		return ""
	}
	var out []string
	for _, item := range types {
		if strings.EqualFold(item, "pref") {
			continue
		}
		out = append(out, item)
	}
	return strings.Join(out, ", ")
}

func vCardPref(line vcardLine) bool {
	for key, values := range line.Params {
		if strings.EqualFold(key, "PREF") {
			return true
		}
		for _, value := range values {
			if strings.EqualFold(value, "pref") {
				return true
			}
		}
	}
	return false
}

func part(parts []string, idx int) string {
	if idx < 0 || idx >= len(parts) {
		return ""
	}
	return strings.TrimSpace(parts[idx])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func primaryEmail(c store.Contact) string {
	for _, email := range c.Emails {
		if email.IsPrimary && strings.TrimSpace(email.Email) != "" {
			return strings.TrimSpace(email.Email)
		}
	}
	for _, email := range c.Emails {
		if strings.TrimSpace(email.Email) != "" {
			return strings.TrimSpace(email.Email)
		}
	}
	return ""
}
