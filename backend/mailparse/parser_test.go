// File overview: Tests for MIME parsing behavior.

package mailparse

import (
	"archive/zip"
	"bytes"
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

func TestParseCleansIndexedCSSNestedRuleWithoutPanic(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender.test",
		"To: archive.test",
		"Subject: nested css",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<html><body><p>Before</p><style>notcss{` + strings.Repeat("x", 2100) + `.rule{color:red}}</style><p>After</p></body></html>`,
	}, "\r\n")
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Text, "Before") || !strings.Contains(parsed.Text, "After") {
		t.Fatalf("text = %q", parsed.Text)
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

func TestDOCXAttachmentSearchableTextExtractsZippedXML(t *testing.T) {
	data := officeZipFixture(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="urn:test"><w:body><w:p><w:r><w:t>Pillars of Community docx text</w:t></w:r></w:p></w:body></w:document>`,
	})

	text := Attachment{
		Filename:    "community.docx",
		ContentType: "application/octet-stream",
		Data:        data,
	}.SearchableText()

	if !strings.Contains(text, "Pillars of Community docx text") {
		t.Fatalf("searchable text = %q", text)
	}
}

func TestODSAttachmentSearchableTextExtractsContentXML(t *testing.T) {
	data := officeZipFixture(t, map[string]string{
		"content.xml": `<office:document-content xmlns:office="urn:office" xmlns:table="urn:table" xmlns:text="urn:text"><office:body><office:spreadsheet><table:table><table:table-row><table:table-cell><text:p>Quarterly ODS forecast needle</text:p></table:table-cell></table:table-row></table:table></office:spreadsheet></office:body></office:document-content>`,
	})

	text := Attachment{
		Filename:    "forecast.ods",
		ContentType: "application/octet-stream",
		Data:        data,
	}.SearchableText()

	if !strings.Contains(text, "Quarterly ODS forecast needle") {
		t.Fatalf("searchable text = %q", text)
	}
}

func TestDOCAttachmentSearchableTextUsesExtractor(t *testing.T) {
	originalExtractor := docTextExtractor
	defer func() { docTextExtractor = originalExtractor }()
	var received string
	docTextExtractor = func(data []byte) (string, error) {
		received = string(data)
		return "Legacy Word document needle", nil
	}

	text := Attachment{
		Filename:    "legacy.doc",
		ContentType: "application/octet-stream",
		Data:        []byte("doc fixture"),
	}.SearchableText()

	if received != "doc fixture" {
		t.Fatalf("extractor received %q", received)
	}
	if !strings.Contains(text, "Legacy Word document needle") {
		t.Fatalf("searchable text = %q", text)
	}
}

func TestPDFAttachmentSearchableTextUsesExtractor(t *testing.T) {
	originalExtractor := pdfTextExtractor
	defer func() { pdfTextExtractor = originalExtractor }()
	var received string
	pdfTextExtractor = func(data []byte) (string, error) {
		received = string(data)
		return "Pillars of Community annual report", nil
	}

	text := Attachment{
		Filename:    "community.pdf",
		ContentType: "application/octet-stream",
		Data:        []byte("%PDF fixture"),
	}.SearchableText()

	if received != "%PDF fixture" {
		t.Fatalf("extractor received %q", received)
	}
	if !strings.Contains(text, "Pillars of Community") {
		t.Fatalf("searchable text = %q", text)
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
func TestParseInlinePGPSignedBodyUsesClearTextOnly(t *testing.T) {
	raw := strings.Join([]string{
		"From: sender@example.test",
		"To: archive@example.test",
		"Subject: signed text",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"-----BEGIN PGP SIGNED MESSAGE-----",
		"Hash: SHA512",
		"",
		"This is a signed message",
		"-----BEGIN PGP SIGNATURE-----",
		"",
		"wrfakebase64",
		"-----END PGP SIGNATURE-----",
	}, "\r\n")

	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.IsSigned {
		t.Fatal("message was not marked signed")
	}
	if parsed.Text != "This is a signed message" {
		t.Fatalf("parsed text = %q", parsed.Text)
	}
	if strings.Contains(parsed.Text, "BEGIN PGP") || strings.Contains(parsed.Text, "SIGNATURE") {
		t.Fatalf("parsed text kept signature armor: %q", parsed.Text)
	}

	text, html, err := ParseDisplayBody(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if html != "" {
		t.Fatalf("html = %q", html)
	}
	if text != "This is a signed message" {
		t.Fatalf("display text = %q", text)
	}
}

func officeZipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
