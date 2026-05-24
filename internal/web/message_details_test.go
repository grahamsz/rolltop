package web

import (
	"net/mail"
	"testing"
)

func TestOneClickUnsubscribeURLRequiresRFC8058PostHeader(t *testing.T) {
	header := mail.Header{
		"List-Unsubscribe": []string{`<https://example.test/unsub>`},
	}
	if _, ok := oneClickUnsubscribeURL(header); ok {
		t.Fatal("expected one-click unsubscribe to require List-Unsubscribe-Post")
	}
}

func TestOneClickUnsubscribeURLPrefersHTTPSCandidate(t *testing.T) {
	header := mail.Header{
		"List-Unsubscribe":      []string{`<mailto:leave@example.test>, <https://example.test/unsub>`},
		"List-Unsubscribe-Post": []string{`List-Unsubscribe=One-Click`},
	}
	u, ok := oneClickUnsubscribeURL(header)
	if !ok {
		t.Fatal("expected one-click unsubscribe URL")
	}
	if u.String() != "https://example.test/unsub" {
		t.Fatalf("unsubscribe URL = %q", u.String())
	}
}
