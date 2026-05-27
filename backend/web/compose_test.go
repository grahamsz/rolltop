// File overview: Tests for compose and reply helpers.

package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"mailmirror/backend/blob"
	"mailmirror/backend/store"
)

func TestForwardComposePrefersSanitizedHTML(t *testing.T) {
	form := forwardComposeForm(store.MessageRecord{
		FromAddr: `"Sender" <sender@example.test>`,
		ToAddr:   "me@example.test",
		Subject:  "Travel plans",
		Date:     time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC),
		BodyText: "div, p, h1 { font-family: Arial } Visible fallback",
		BodyHTML: `<html><head><style>div, p, h1 { font-family: Arial }</style><script>alert(1)</script></head><body><p>Hello <strong>there</strong></p><img src="https://tracker.example/open.png" onload="bad()"></body></html>`,
	})
	if !strings.Contains(form.BodyHTML, "<strong>there</strong>") {
		t.Fatalf("forward html lost body: %q", form.BodyHTML)
	}
	if !strings.Contains(form.BodyHTML, `src="https://tracker.example/open.png"`) {
		t.Fatalf("forward html lost image src: %q", form.BodyHTML)
	}
	for _, bad := range []string{"font-family", "<script", "<style", "onload"} {
		if strings.Contains(strings.ToLower(form.BodyHTML), strings.ToLower(bad)) {
			t.Fatalf("forward html contains %q: %s", bad, form.BodyHTML)
		}
	}
	if strings.Contains(form.Body, "font-family") {
		t.Fatalf("forward text used CSS fallback: %q", form.Body)
	}
	if !strings.Contains(form.Body, "Hello there") {
		t.Fatalf("forward text missing visible body: %q", form.Body)
	}
}

func TestForwardComposeHydratesHTMLFromRawBlob(t *testing.T) {
	raw := strings.Join([]string{
		"From: \"Peak Design\" <info@peakdesign.com>",
		"To: me@example.test",
		"Subject: Ten bucks",
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<html><body><h1>Enjoy <strong>$10 off</strong></h1><p><a href="https://example.test/deal">Open the deal</a></p><img src="https://example.test/hero.jpg" alt="Hero"></body></html>`,
	}, "\r\n")
	blobs := blob.New(t.TempDir())
	saved, err := blobs.SaveRawMessage(7, 1, "INBOX", 42, []byte(raw))
	if err != nil {
		t.Fatalf("save raw message: %v", err)
	}
	server := &Server{blobs: blobs}
	form := server.forwardComposeFormForMessage(context.Background(), 7, store.MessageRecord{
		UserID:   7,
		BlobPath: saved.Path,
		FromAddr: `"Peak Design" <info@peakdesign.com>`,
		ToAddr:   "me@example.test",
		Subject:  "Ten bucks",
		BodyText: "Preview [Open the deal](https://example.test/deal)",
	})
	if !strings.Contains(form.BodyHTML, "<strong>$10 off</strong>") {
		t.Fatalf("forward html did not use raw blob html: %q", form.BodyHTML)
	}
	if !strings.Contains(form.BodyHTML, `href="https://example.test/deal"`) {
		t.Fatalf("forward html lost link href: %q", form.BodyHTML)
	}
	if !strings.Contains(form.BodyHTML, `src="https://example.test/hero.jpg"`) {
		t.Fatalf("forward html lost image src: %q", form.BodyHTML)
	}
	if strings.Contains(form.Body, "[Open the deal]") {
		t.Fatalf("forward text used indexed markdown preview instead of raw body: %q", form.Body)
	}
}

func TestReplyAllRecipientsExcludeOwnAddress(t *testing.T) {
	own := map[string]bool{"me@example.test": true}
	msg := store.MessageRecord{
		FromAddr: `"Sender" <sender@example.test>`,
		ToAddr:   `"Graham" <me@example.test>`,
		CCAddr:   `"Project" <project@example.test>, me@example.test`,
		Subject:  "Plan",
	}
	form := replyAllComposeForm(msg, []store.MessageRecord{msg}, own)
	if form.To != `"Sender" <sender@example.test>` {
		t.Fatalf("reply-all To = %q", form.To)
	}
	if form.Cc != `"Project" <project@example.test>` {
		t.Fatalf("reply-all Cc = %q", form.Cc)
	}
	if !canReplyAll(msg, []store.MessageRecord{msg}, own) {
		t.Fatalf("expected reply-all to be available when an external cc recipient exists")
	}
}

func TestReplyAllFromOwnMessageTargetsExternalRecipients(t *testing.T) {
	own := map[string]bool{"me@example.test": true}
	msg := store.MessageRecord{
		FromAddr: `"Graham" <me@example.test>`,
		ToAddr:   `"Charity" <charity@example.test>`,
		CCAddr:   `"Project" <project@example.test>, me@example.test`,
		Subject:  "Plan",
	}
	form := replyAllComposeForm(msg, []store.MessageRecord{msg}, own)
	if form.To != `"Charity" <charity@example.test>` {
		t.Fatalf("reply-all To = %q", form.To)
	}
	if form.Cc != `"Project" <project@example.test>` {
		t.Fatalf("reply-all Cc = %q", form.Cc)
	}
}

func TestCanReplyAllFalseForSingleExternalSender(t *testing.T) {
	own := map[string]bool{"me@example.test": true}
	msg := store.MessageRecord{
		FromAddr: `sender@example.test`,
		ToAddr:   `me@example.test`,
		Subject:  "Plan",
	}
	if canReplyAll(msg, []store.MessageRecord{msg}, own) {
		t.Fatalf("reply-all should be hidden when reply would only address one external sender")
	}
}
