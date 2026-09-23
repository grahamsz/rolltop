// File overview: Self-contained vCard 3.0/4.0 parser for CardDAV sync.
// Unfolding, line parsing, and unescaping mirror backend/web/contact_vcf.go so
// synced contacts look identical to contacts imported through the UI. This
// package intentionally does not import backend/web.

package main

import (
	"strings"

	"rolltop/backend/store"
)

// vcardProperty is one unfolded content line of a vCard.
type vcardProperty struct {
	Name   string
	Params map[string][]string
	Value  string
}

// parsedVCard is a single vCard plus its UID, used to key the sync mapping.
type parsedVCard struct {
	UID     string
	Contact store.Contact
}

// parseVCards splits raw vCard data into cards. Cards without a UID are
// skipped: the sync mapping table keys on UID, so UID-less cards could not be
// updated or deleted on later runs.
func parseVCards(data []byte) []parsedVCard {
	lines := unfoldVCardLines(string(data))
	var out []parsedVCard
	var card []vcardProperty
	inCard := false
	for _, raw := range lines {
		prop, ok := parseVCardProperty(raw)
		if !ok {
			continue
		}
		switch prop.Name {
		case "BEGIN":
			if strings.EqualFold(strings.TrimSpace(prop.Value), "VCARD") {
				inCard = true
				card = nil
			}
		case "END":
			if inCard && strings.EqualFold(strings.TrimSpace(prop.Value), "VCARD") {
				if parsed, ok := contactFromVCard(card); ok {
					out = append(out, parsed)
				}
				inCard = false
				card = nil
			}
		default:
			if inCard {
				card = append(card, prop)
			}
		}
	}
	return out
}

// unfoldVCardLines joins folded content lines per RFC 6350 section 3.2.
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

func parseVCardProperty(raw string) (vcardProperty, bool) {
	raw = strings.TrimRight(raw, "\n")
	if strings.TrimSpace(raw) == "" {
		return vcardProperty{}, false
	}
	idx := strings.Index(raw, ":")
	if idx < 0 {
		return vcardProperty{}, false
	}
	head := raw[:idx]
	value := raw[idx+1:]
	parts := strings.Split(head, ";")
	prop := vcardProperty{Name: strings.ToUpper(strings.TrimSpace(parts[0])), Params: map[string][]string{}, Value: value}
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
				prop.Params[key] = append(prop.Params[key], item)
			}
		}
	}
	return prop, true
}

func contactFromVCard(props []vcardProperty) (parsedVCard, bool) {
	var out parsedVCard
	var c store.Contact
	for _, prop := range props {
		value := unescapeVCard(prop.Value)
		switch prop.Name {
		case "UID":
			out.UID = strings.TrimSpace(value)
		case "FN":
			c.DisplayName = strings.TrimSpace(value)
		case "N":
			parts := splitVCardList(prop.Value, 5)
			c.FamilyName = vcardPart(parts, 0)
			c.GivenName = vcardPart(parts, 1)
			c.AdditionalName = vcardPart(parts, 2)
			c.NamePrefix = vcardPart(parts, 3)
			c.NameSuffix = vcardPart(parts, 4)
		case "NICKNAME":
			c.Nickname = strings.TrimSpace(value)
		case "ORG":
			parts := splitVCardList(prop.Value, 2)
			c.Organization = vcardPart(parts, 0)
			c.Department = vcardPart(parts, 1)
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
				c.Emails = append(c.Emails, store.ContactEmail{Label: vcardLabel(prop), Email: strings.TrimSpace(value), IsPrimary: vcardPref(prop)})
			}
		case "TEL":
			if strings.TrimSpace(value) != "" {
				c.Phones = append(c.Phones, store.ContactPhone{Label: vcardLabel(prop), Number: strings.TrimSpace(value), IsPrimary: vcardPref(prop)})
			}
		case "ADR":
			parts := splitVCardList(prop.Value, 7)
			addr := store.ContactAddress{
				Label:      vcardLabel(prop),
				Street:     vcardPart(parts, 2),
				Locality:   vcardPart(parts, 3),
				Region:     vcardPart(parts, 4),
				PostalCode: vcardPart(parts, 5),
				Country:    vcardPart(parts, 6),
				IsPrimary:  vcardPref(prop),
			}
			if strings.TrimSpace(addr.Street+addr.Locality+addr.Region+addr.PostalCode+addr.Country) != "" {
				c.Addresses = append(c.Addresses, addr)
			}
		case "URL":
			if strings.TrimSpace(value) != "" {
				c.URLs = append(c.URLs, store.ContactURL{Label: vcardLabel(prop), URL: strings.TrimSpace(value), IsPrimary: vcardPref(prop)})
			}
		}
	}
	if c.DisplayName == "" {
		c.DisplayName = strings.TrimSpace(strings.Join(strings.Fields(c.GivenName+" "+c.FamilyName), " "))
	}
	if c.DisplayName == "" && len(c.Emails) > 0 {
		c.DisplayName = c.Emails[0].Email
	}
	out.Contact = c
	return out, out.UID != ""
}

// splitVCardList splits a structured vCard value on unescaped semicolons.
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

func vcardLabel(prop vcardProperty) string {
	types := prop.Params["TYPE"]
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

func vcardPref(prop vcardProperty) bool {
	for key, values := range prop.Params {
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

func vcardPart(parts []string, idx int) string {
	if idx < 0 || idx >= len(parts) {
		return ""
	}
	return strings.TrimSpace(parts[idx])
}
