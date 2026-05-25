// File overview: Tests for vCard import/export behavior.

package web

import (
	"strings"
	"testing"
)

func TestParseVCardsSupportsMultipleContacts(t *testing.T) {
	contacts, err := parseVCards([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane Sender\r\nN:Sender;Jane;;;\r\nEMAIL;TYPE=work:jane@example.test\r\nTEL;TYPE=cell,pref:+1 555 0101\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Example Org\r\nORG:Example;Ops\r\nEMAIL:ops@example.test\r\nEND:VCARD\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 {
		t.Fatalf("contacts = %+v", contacts)
	}
	if contacts[0].DisplayName != "Jane Sender" || contacts[0].GivenName != "Jane" || contacts[0].FamilyName != "Sender" {
		t.Fatalf("first contact = %+v", contacts[0])
	}
	if len(contacts[0].Emails) != 1 || contacts[0].Emails[0].Email != "jane@example.test" {
		t.Fatalf("first emails = %+v", contacts[0].Emails)
	}
	if len(contacts[0].Phones) != 1 || !contacts[0].Phones[0].IsPrimary {
		t.Fatalf("first phones = %+v", contacts[0].Phones)
	}
	if contacts[1].Organization != "Example" || contacts[1].Department != "Ops" {
		t.Fatalf("second contact = %+v", contacts[1])
	}
}

func TestWriteVCardsEscapesAndIncludesMultipleContacts(t *testing.T) {
	contacts, err := parseVCards([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Comma\\, Name\r\nEMAIL:name@example.test\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:3.0\r\nFN:Notes\r\nNOTE:line one\\nline two\r\nEMAIL:notes@example.test\r\nEND:VCARD\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(writeVCards(contacts))
	if strings.Count(out, "BEGIN:VCARD") != 2 {
		t.Fatalf("vcf = %s", out)
	}
	for _, want := range []string{"FN:Comma\\, Name", "EMAIL:name@example.test", "NOTE:line one\\nline two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("vcf missing %q: %s", want, out)
		}
	}
}
