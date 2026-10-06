// File overview: Tests for the CardDAV vCard parser.

package main

import (
	"strings"
	"testing"

	"rolltop/backend/store"
)

func parseFirstCard(t *testing.T, data string) parsedVCard {
	t.Helper()
	cards := parseVCards([]byte(data))
	if len(cards) != 1 {
		t.Fatalf("parseVCards returned %d cards, want 1", len(cards))
	}
	return cards[0]
}

func TestParseVCardFullContact(t *testing.T) {
	card := parseFirstCard(t, "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:abc-123\r\nFN:Adrienne Mchugh\r\n"+
		"N:Mchugh;Adrienne;Marie;Dr.;Jr.\r\nNICKNAME:Ali\r\nORG:Waytta Ltd;Engineering\r\nTITLE:Dev Lead\r\n"+
		"EMAIL;TYPE=WORK:ali@waytta.com\r\nTEL;TYPE=CELL:+353 83 123 4567\r\nADR;TYPE=HOME:;;1 Harbour Rd;Bray;Co Wicklow;A63 X123;Ireland\r\n"+
		"URL;TYPE=HOME:https://example.com/ali\r\nBDAY:1985-03-14\r\nNOTE:Met at the sailing club\r\nCATEGORIES:friends,sailing\r\nEND:VCARD\r\n")
	c := card.Contact
	if card.UID != "abc-123" {
		t.Fatalf("UID = %q, want abc-123", card.UID)
	}
	if c.DisplayName != "Adrienne Mchugh" {
		t.Fatalf("DisplayName = %q", c.DisplayName)
	}
	if c.GivenName != "Adrienne" || c.FamilyName != "Mchugh" || c.AdditionalName != "Marie" ||
		c.NamePrefix != "Dr." || c.NameSuffix != "Jr." {
		t.Fatalf("name parts = %+v", []string{c.NamePrefix, c.GivenName, c.AdditionalName, c.FamilyName, c.NameSuffix})
	}
	if c.Nickname != "Ali" || c.Organization != "Waytta Ltd" || c.Department != "Engineering" || c.JobTitle != "Dev Lead" {
		t.Fatalf("org parts = %+v", []string{c.Nickname, c.Organization, c.Department, c.JobTitle})
	}
	if len(c.Emails) != 1 || c.Emails[0].Email != "ali@waytta.com" || !strings.Contains(c.Emails[0].Label, "WORK") {
		t.Fatalf("emails = %+v", c.Emails)
	}
	if len(c.Phones) != 1 || c.Phones[0].Number != "+353 83 123 4567" || !strings.Contains(c.Phones[0].Label, "CELL") {
		t.Fatalf("phones = %+v", c.Phones)
	}
	if len(c.Addresses) != 1 || c.Addresses[0].Street != "1 Harbour Rd" || c.Addresses[0].Locality != "Bray" ||
		c.Addresses[0].Country != "Ireland" || !strings.Contains(c.Addresses[0].Label, "HOME") {
		t.Fatalf("addresses = %+v", c.Addresses)
	}
	if len(c.URLs) != 1 || c.URLs[0].URL != "https://example.com/ali" {
		t.Fatalf("urls = %+v", c.URLs)
	}
	if c.Birthday != "1985-03-14" || c.Notes != "Met at the sailing club" || c.Categories != "friends,sailing" {
		t.Fatalf("misc = %+v", []string{c.Birthday, c.Notes, c.Categories})
	}
}

func TestParseVCardFoldedLines(t *testing.T) {
	card := parseFirstCard(t, "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:fold-1\r\n"+
		"NOTE:This note is fol\r\n ded across two lines and kee\r\n ps the content\r\nEND:VCARD\r\n")
	if card.Contact.Notes != "This note is folded across two lines and keeps the content" {
		t.Fatalf("notes = %q", card.Contact.Notes)
	}
}

func TestParseVCardEscapes(t *testing.T) {
	card := parseFirstCard(t, "BEGIN:VCARD\nVERSION:3.0\nUID:esc-1\nFN:Comma\\, Name\\nNewline\\\\Backslash\nEND:VCARD\n")
	if card.Contact.DisplayName != "Comma, Name\nNewline\\Backslash" {
		t.Fatalf("display name = %q", card.Contact.DisplayName)
	}
}

func TestParseVCardsSkipsUIDLess(t *testing.T) {
	cards := parseVCards([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:No UID Here\r\nEND:VCARD\r\n"))
	if len(cards) != 0 {
		t.Fatalf("parseVCards returned %d cards, want 0", len(cards))
	}
}

func TestParseVCardsMultiple(t *testing.T) {
	cards := parseVCards([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:one\r\nFN:One\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:two\r\nFN:Two\r\nEND:VCARD\r\n"))
	if len(cards) != 2 || cards[0].UID != "one" || cards[1].UID != "two" {
		t.Fatalf("cards = %+v", cards)
	}
}

func TestParseVCardPrefIsPrimary(t *testing.T) {
	card := parseFirstCard(t, "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:pref-1\r\n"+
		"EMAIL;TYPE=WORK:secondary@work.com\r\nEMAIL;TYPE=PREF,WORK:primary@work.com\r\n"+
		"TEL;TYPE=PREF,VOICE:+111\r\nTEL;TYPE=HOME:+222\r\nEND:VCARD\r\n")
	if len(card.Contact.Emails) != 2 {
		t.Fatalf("emails = %+v", card.Contact.Emails)
	}
	if card.Contact.Emails[0].IsPrimary {
		t.Fatalf("first email should not be primary: %+v", card.Contact.Emails[0])
	}
	if card.Contact.Emails[1].Email != "primary@work.com" || !card.Contact.Emails[1].IsPrimary {
		t.Fatalf("primary email = %+v", card.Contact.Emails[1])
	}
	if card.Contact.Phones[0].Number != "+111" || !card.Contact.Phones[0].IsPrimary {
		t.Fatalf("primary phone = %+v", card.Contact.Phones[0])
	}
	if card.Contact.Phones[1].IsPrimary {
		t.Fatalf("second phone should not be primary: %+v", card.Contact.Phones[1])
	}
}

func TestContactIsEmpty(t *testing.T) {
	if !contactIsEmpty(store.Contact{}) {
		t.Fatal("zero contact should be empty")
	}
	if contactIsEmpty(store.Contact{DisplayName: "x"}) {
		t.Fatal("named contact should not be empty")
	}
	if contactIsEmpty(store.Contact{Emails: []store.ContactEmail{{Email: "x@y.z"}}}) {
		t.Fatal("contact with email should not be empty")
	}
}

func TestMergeSyncedContact(t *testing.T) {
	existing := store.Contact{
		DisplayName: "Existing Name", GivenName: "Existing", JobTitle: "Engineer",
		Emails: []store.ContactEmail{{Email: "a@x.com"}},
		Phones: []store.ContactPhone{{Number: "+111"}},
		Notes:  "keep me",
	}
	incoming := store.Contact{
		DisplayName: "New Name", GivenName: "Incoming", Organization: "Waytta",
		Emails: []store.ContactEmail{{Email: "a@x.com"}, {Email: "b@x.com"}},
		Phones: []store.ContactPhone{{Number: "+111"}, {Number: "+222"}},
		Notes:  "overwrite me",
	}
	merged := mergeSyncedContact(existing, incoming)
	// Empty fields are filled.
	if merged.Organization != "Waytta" {
		t.Fatalf("organization = %q", merged.Organization)
	}
	// Filled fields are kept.
	if merged.DisplayName != "Existing Name" || merged.GivenName != "Existing" || merged.JobTitle != "Engineer" {
		t.Fatalf("filled fields changed: %+v", merged)
	}
	if merged.Notes != "keep me" {
		t.Fatalf("notes = %q", merged.Notes)
	}
	// Children are unioned without duplicates.
	if len(merged.Emails) != 2 || len(merged.Phones) != 2 {
		t.Fatalf("unioned = %+v / %+v", merged.Emails, merged.Phones)
	}
}
