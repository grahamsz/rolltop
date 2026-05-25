// File overview: Tests for MIME parsing behavior.

package mailparse

import (
	"strings"
	"testing"
)

func TestParseToleratesUnexpectedEOFinMultipart(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: broken multipart",
		"Content-Type: multipart/mixed; boundary=broken",
		"",
		"--broken",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body before abrupt end",
	}, "\r\n")
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject != "broken multipart" {
		t.Fatalf("subject = %q", parsed.Subject)
	}
	if !strings.Contains(parsed.Text, "body before abrupt end") {
		t.Fatalf("text = %q", parsed.Text)
	}
}

func TestParsePreservesPlainTextLineBreaks(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: plain text",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"First line.",
		"",
		"Second paragraph.",
		"> quoted line",
	}, "\r\n")
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := "First line.\n\nSecond paragraph.\n> quoted line"
	if parsed.Text != want {
		t.Fatalf("text = %q, want %q", parsed.Text, want)
	}
}

func TestParseCleansIndexedCSSBraceResidue(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender.test",
		"To: archive.test",
		"Subject: secure code",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<html><body>@media screen and (max-width:600px){.ExternalClass{width:100%}.preheader{display:none!important}}}} <p>Hi, For your security, never share this code with anyone.</p></body></html>`,
	}, "\r\n")
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := "Hi, For your security, never share this code with anyone."
	if parsed.Text != want {
		t.Fatalf("text = %q, want %q", parsed.Text, want)
	}
}

func TestParseDecodesISO2022JPSubjectAndBody(t *testing.T) {
	body := "\x1b$B4|4V8BDj%]%$%s%H\x1b(B"
	raw := strings.Join([]string{
		"From: =?ISO-2022-JP?B?GyRCJUYlOSVIGyhC?= <sender@example.test>",
		"To: archive@example.test",
		"Subject: =?ISO-2022-JP?B?GyRCJV0lJCVzJUg8Ojh6JE4kKkNOJGkkOxsoQg==?=",
		"Content-Type: text/plain; charset=ISO-2022-JP",
		"",
		body,
	}, "\r\n")
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject != "ポイント失効のお知らせ" {
		t.Fatalf("subject = %q", parsed.Subject)
	}
	if parsed.From != `"テスト" <sender@example.test>` {
		t.Fatalf("from = %q", parsed.From)
	}
	if parsed.Text != "期間限定ポイント" {
		t.Fatalf("text = %q", parsed.Text)
	}
}

func TestParseMarksCIDImagesInline(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender.test",
		"To: archive.test",
		"Subject: inline image",
		"Content-Type: multipart/related; boundary=rel",
		"",
		"--rel",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<p>Hello</p><img src="cid:sig-image">`,
		"--rel",
		"Content-Type: image/png; name=\"signature.png\"",
		"Content-Disposition: inline; filename=\"signature.png\"",
		"Content-ID: <sig-image>",
		"Content-Transfer-Encoding: base64",
		"",
		"iVBORw0KGgo=",
		"--rel--",
	}, "\r\n")
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Files) != 1 {
		t.Fatalf("files = %d", len(parsed.Files))
	}
	if !parsed.Files[0].IsInline {
		t.Fatalf("inline image was not marked inline: %+v", parsed.Files[0])
	}
}

func TestParseDisplayBodyPreservesPlainTextLineBreaks(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: plain text",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"First line.",
		"",
		"Second paragraph.",
	}, "\r\n")
	text, html, err := ParseDisplayBody(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if html != "" {
		t.Fatalf("html = %q", html)
	}
	want := "First line.\n\nSecond paragraph."
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}
