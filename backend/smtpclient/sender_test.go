// File overview: Tests for SMTP send behavior.

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

func TestBuildRawWithAttachmentUsesMultipartMixed(t *testing.T) {
	raw, _, err := BuildRaw(Message{
		From:      "sender@example.test",
		To:        []string{"to@example.test"},
		Subject:   "Attachment",
		BodyText:  "See attached",
		MessageID: "<attach@example.test>",
		Date:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
		Attachments: []Attachment{{
			Filename:    "report.txt",
			ContentType: "text/plain",
			Data:        []byte("hello attachment"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lower := bytes.ToLower(raw)
	for _, want := range [][]byte{
		[]byte("content-type: multipart/mixed"),
		[]byte("content-disposition: attachment"),
		[]byte("filename=report.txt"),
		[]byte("content-transfer-encoding: base64"),
		[]byte("aGVsbG8gYXR0YWNobWVudA=="),
	} {
		if !bytes.Contains(lower, bytes.ToLower(want)) {
			t.Fatalf("raw message missing %q:\n%s", want, raw)
		}
	}
}

func TestBuildRawWithInlineAttachmentUsesRelatedContentID(t *testing.T) {
	raw, _, err := BuildRaw(Message{
		From:      "sender@example.test",
		To:        []string{"to@example.test"},
		Subject:   "Inline",
		BodyText:  "Inline image",
		BodyHTML:  `<p>Inline <img src="cid:image-1@compose.local"></p>`,
		MessageID: "<inline@example.test>",
		Date:      time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
		Attachments: []Attachment{{
			Filename:    "image.png",
			ContentType: "image/png",
			ContentID:   "image-1@compose.local",
			Inline:      true,
			Data:        []byte{0x89, 0x50, 0x4e, 0x47},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lower := bytes.ToLower(raw)
	for _, want := range [][]byte{
		[]byte("content-type: multipart/related"),
		[]byte("content-id: <image-1@compose.local>"),
		[]byte("content-disposition: inline"),
		[]byte("content-type: image/png"),
	} {
		if !bytes.Contains(lower, bytes.ToLower(want)) {
			t.Fatalf("raw message missing %q:\n%s", want, raw)
		}
	}
}
