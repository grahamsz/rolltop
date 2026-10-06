// File overview: Tests for message detail assembly and metadata behavior.

package web

import (
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"testing"

	"rolltop/internal/testlog"
)

func TestOneClickUnsubscribeURLRequiresRFC8058PostHeader(t *testing.T) {
	header := mail.Header{
		"List-Unsubscribe": []string{`<https://example.test/unsub>`},
	}
	if _, ok := oneClickUnsubscribeURL(header); ok {
		t.Fatal("expected one-click unsubscribe to require List-Unsubscribe-Post")
	}
}

func TestUnsubscribeFailureLogsNoURLCapabilities(t *testing.T) {
	logs := testlog.Capture(t)
	target, err := url.Parse("https://user:password@example.test/path-secret?token=query-secret")
	if err != nil {
		t.Fatal(err)
	}
	err = &url.Error{Op: "Post", URL: target.String(), Err: errors.New("redirect to https://example.test/nested-secret")}
	logUnsubscribeFailure("transport", target, err)
	for _, secret := range []string{"user", "password", "path-secret", "query-secret", "nested-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log leaked URL capability %q: %q", secret, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "example.test") || !strings.Contains(logs.String(), "*url.Error") {
		t.Fatalf("log omitted host/error type: %q", logs.String())
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
