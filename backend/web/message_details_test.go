// File overview: Tests for message detail assembly and metadata behavior.

package web

import (
	"net/mail"
	"strings"
	"testing"

	"rolltop/backend/store"
)

func TestMessageHeaderDetailsIncludeReportedAuthentication(t *testing.T) {
	header := readRawMessageHeader(strings.NewReader(strings.Join([]string{
		"Authentication-Results: mx.receiver.example; spf=pass smtp.mailfrom=sender.example; dkim=fail header.d=sender.example; dmarc=none header.from=sender.example",
		"",
		"body",
	}, "\r\n")))
	details := messageHeaderDetailsFromHeader(header, store.MessageRecord{})

	want := map[string]string{
		"SPF":   "pass (reported by the Authentication-Results header; not independently verified by Rolltop)",
		"DKIM":  "fail (reported by the Authentication-Results header; not independently verified by Rolltop)",
		"DMARC": "none (reported by the Authentication-Results header; not independently verified by Rolltop)",
	}
	for _, detail := range details {
		if expected, ok := want[detail.Label]; ok {
			if detail.Value != expected {
				t.Errorf("%s detail = %q, want %q", detail.Label, detail.Value, expected)
			}
			delete(want, detail.Label)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing reported authentication details: %+v; got %+v", want, details)
	}
}

func TestReportedAuthenticationHeaderDetailsLabelReceivedSPFSource(t *testing.T) {
	details := reportedAuthenticationHeaderDetails(reportedAuthentication{
		SPF: &reportedAuthenticationResult{Result: "softfail", Source: "received-spf"},
	})
	if len(details) != 1 || details[0].Label != "SPF" || !strings.Contains(details[0].Value, "Received-SPF header") {
		t.Fatalf("Received-SPF details = %+v", details)
	}
}

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
