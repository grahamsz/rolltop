package web

import (
	"strings"
	"testing"
	"time"

	"mailmirror/internal/store"
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
	for _, bad := range []string{"font-family", "<script", "<style", "tracker.example", "onload"} {
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
