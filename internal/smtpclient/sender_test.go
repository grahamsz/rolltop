package smtpclient

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestBuildRawOmitsBccHeaderAndIncludesRecipients(t *testing.T) {
	raw, recipients, err := BuildRaw(Message{
		From:      "Sender <sender@example.test>",
		To:        []string{"one@example.test, Two <two@example.test>"},
		Cc:        []string{"cc@example.test"},
		Bcc:       []string{"hidden@example.test"},
		Subject:   "Hello",
		BodyText:  "Line one\nLine two",
		MessageID: "<id@example.test>",
		Date:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	gotRecipients := strings.Join(recipients, ",")
	for _, want := range []string{"one@example.test", "two@example.test", "cc@example.test", "hidden@example.test"} {
		if !strings.Contains(gotRecipients, want) {
			t.Fatalf("recipients %q missing %q", gotRecipients, want)
		}
	}
	if bytes.Contains(bytes.ToLower(raw), []byte("\r\nbcc:")) {
		t.Fatalf("raw message leaked Bcc header:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("Message-ID: <id@example.test>\r\n")) {
		t.Fatalf("raw message missing Message-ID:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("Line one\r\nLine two\r\n")) {
		t.Fatalf("raw message body not CRLF normalized:\n%s", raw)
	}
}

func TestBuildRawRejectsHeaderInjection(t *testing.T) {
	raw, _, err := BuildRaw(Message{
		From:      "sender@example.test",
		To:        []string{"to@example.test"},
		Subject:   "Hello\r\nBcc: attacker@example.test",
		BodyText:  "Body",
		MessageID: "<id@example.test>",
		Date:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("\r\nBcc: attacker@example.test")) {
		t.Fatalf("raw message allowed injected header:\n%s", raw)
	}
}

func TestBuildRawWithHTMLBodyUsesMultipartAlternative(t *testing.T) {
	raw, _, err := BuildRaw(Message{
		From:      "sender@example.test",
		To:        []string{"to@example.test"},
		Subject:   "Rich",
		BodyText:  "Hello rich mail",
		BodyHTML:  "<p><strong>Hello</strong> rich mail</p>",
		MessageID: "<rich@example.test>",
		Date:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	lower := bytes.ToLower(raw)
	if !bytes.Contains(lower, []byte("content-type: multipart/alternative")) {
		t.Fatalf("raw message is not multipart alternative:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte("Hello rich mail")) || !bytes.Contains(raw, []byte("<strong>Hello</strong>")) {
		t.Fatalf("raw message missing text/html parts:\n%s", raw)
	}
}
