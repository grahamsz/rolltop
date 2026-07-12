package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

func TestReportedAuthenticationUsesFirstRecognizedHeaderAndLabelsSource(t *testing.T) {
	header := readRawMessageHeader(strings.NewReader(strings.Join([]string{
		"Authentication-Results: mx.receiver.example; spf=pass smtp.mailfrom=sender.example; dkim=fail header.d=sender.example; dmarc=pass header.from=sender.example",
		"Authentication-Results: older-hop.example; spf=fail; dkim=pass; dmarc=fail",
		"Received-SPF: fail (older fallback)",
		"DKIM-Signature: v=1; d=sender.example; s=test; b=fake",
		"",
		"body",
	}, "\r\n")))
	reported := reportedAuthenticationFromHeader(header)
	assertReportedAuthentication(t, reported.SPF, "pass", "authentication-results")
	assertReportedAuthentication(t, reported.DKIM, "fail", "authentication-results")
	assertReportedAuthentication(t, reported.DMARC, "pass", "authentication-results")
}

func TestReportedAuthenticationFallsBackToReceivedSPFWithoutClaimingDKIMVerification(t *testing.T) {
	header := readRawMessageHeader(strings.NewReader(strings.Join([]string{
		"Received-SPF: softfail (receiver.example: transitioning domain)",
		"DKIM-Signature: v=1; d=sender.example; s=test; b=fake",
		"",
		"body",
	}, "\r\n")))
	reported := reportedAuthenticationFromHeader(header)
	assertReportedAuthentication(t, reported.SPF, "softfail", "received-spf")
	if reported.DKIM != nil || reported.DMARC != nil {
		t.Fatalf("signature presence was presented as an authentication result: %+v", reported)
	}
}

func TestMessageSecuritySignalsAreConservativeAndBounded(t *testing.T) {
	header := readRawMessageHeader(strings.NewReader(strings.Join([]string{
		`From: "billing@trusted.example" <notice@evil.example>`,
		"Reply-To: support@different.test",
		"",
		"body",
	}, "\r\n")))
	htmlBody := strings.Join([]string{
		`<a href="https://account.example.com/login">https://www.example.com/security</a>`,
		`<a href="https://bank.example.com@evil.example.net/session"><span>https://bank.example.com/login</span></a>`,
		`<a href="java&#x73;cript:alert(1)">Review</a>`,
		`<a href="data:text/html,bad">Details</a>`,
		`<a href="https://unrelated.example.net">Click here</a>`,
	}, "")
	indicators := messageSecurityIndicatorsFor(header, store.MessageRecord{FromAddr: `"billing@trusted.example" <notice@evil.example>`}, htmlBody)
	assertSecuritySignal(t, indicators.Signals, "sender_display_address_mismatch", "trusted.example", "evil.example", "")
	assertSecuritySignal(t, indicators.Signals, "reply_to_domain_mismatch", "evil.example", "different.test", "")
	assertSecuritySignal(t, indicators.Signals, "link_destination_mismatch", "bank.example.com", "evil.example.net", "")
	assertSecuritySignal(t, indicators.Signals, "risky_link_scheme", "", "", "javascript")
	assertSecuritySignal(t, indicators.Signals, "risky_link_scheme", "", "", "data")
	assertSecuritySignal(t, indicators.Signals, "link_destination_mismatch", "www.example.com", "account.example.com", "")
	if len(indicators.Signals) != 6 {
		t.Fatalf("signals = %+v, want exactly six concrete indicators", indicators.Signals)
	}

	var many strings.Builder
	for idx := 0; idx < maxMessageSecuritySignals+20; idx++ {
		fmt.Fprintf(&many, `<a href="https://target-%d.evil.test">https://display-%d.safe.example</a>`, idx, idx)
	}
	if signals := linkSecuritySignals(many.String()); len(signals) != maxMessageSecuritySignals {
		t.Fatalf("bounded signals = %d, want %d", len(signals), maxMessageSecuritySignals)
	}
}

func TestSecurityDomainsDoNotMergeSharedSuffixTenants(t *testing.T) {
	for _, pair := range [][2]string{
		{"safe.github.io", "evil.github.io"},
		{"bank.example.co.uk", "attacker.co.uk"},
		{"login.example.com", "example.com"},
	} {
		if sameSecurityDomain(pair[0], pair[1]) {
			t.Fatalf("sameSecurityDomain(%q, %q) = true", pair[0], pair[1])
		}
	}
	if !sameSecurityDomain("LOGIN.EXAMPLE.COM.", "login.example.com") {
		t.Fatal("normalized identical hosts were treated as different")
	}
}

func TestLinkSecuritySignalsBoundMalformedInput(t *testing.T) {
	body := `<a href="https://target.evil.test">https://display.safe.example/` + strings.Repeat("x", maxSecurityHTMLBytes*2)
	signals := linkSecuritySignals(body)
	assertSecuritySignal(t, signals, "link_destination_mismatch", "display.safe.example", "target.evil.test", "")
	if len(signals) > maxMessageSecuritySignals {
		t.Fatalf("malformed input produced %d signals, limit is %d", len(signals), maxMessageSecuritySignals)
	}
}

func TestMessageSecurityIndicatorsOmitEmptyJSON(t *testing.T) {
	if got := apiMessageSecurityIndicatorsFrom(messageSecurityIndicators{}); got != nil {
		t.Fatalf("empty indicators converted to %+v, want nil", got)
	}
	payload, err := json.Marshal(apiThreadMessage{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "security_indicators") {
		t.Fatalf("empty security metadata was serialized: %s", payload)
	}
}

func TestMessageSecurityIndicatorsAppearOnlyOnOwningUsersThreadAPI(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "security-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "security-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: owner.ID, Email: owner.Email, Host: "imap.example.test", Port: 993,
		Username: owner.Email, EncryptedPassword: "encrypted", Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, owner.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	bodyHTML := `<p><a href="https://login.evil.test">https://login.trusted.example/account</a></p>`
	raw := strings.Join([]string{
		`From: "billing@trusted.example" <notice@evil.example>`,
		"To: " + owner.Email,
		"Reply-To: recovery@different.test",
		"Subject: Security test",
		"Authentication-Results: mx.receiver.example; spf=fail smtp.mailfrom=evil.example; dkim=pass header.d=evil.example; dmarc=fail header.from=evil.example",
		"Content-Type: text/html; charset=utf-8",
		"",
		bodyHTML,
	}, "\r\n")
	blobStore := blob.New(dir)
	saved, err := blobStore.SaveRawMessage(owner.ID, account.ID, mailbox.Name, 1201, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: owner.ID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: owner.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blobRecord.ID,
		ThreadKey: "security-indicator-thread", Subject: "Security test",
		FromAddr: `"billing@trusted.example" <notice@evil.example>`, ToAddr: owner.Email,
		Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: 1201, Size: saved.Size,
		BlobPath: saved.Path, BodyHTML: bodyHTML, IsRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, blobs: blobStore, mailListCache: newMailListCache()}

	otherReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/messages/%d", message.ID), nil)
	otherReq = otherReq.WithContext(context.WithValue(otherReq.Context(), userContextKey, currentUser{User: other}))
	otherRes := httptest.NewRecorder()
	server.apiMessage(otherRes, otherReq, message.ID)
	if otherRes.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant message status = %d body=%s", otherRes.Code, otherRes.Body.String())
	}
	if strings.Contains(otherRes.Body.String(), "trusted.example") || strings.Contains(otherRes.Body.String(), "evil.example") {
		t.Fatalf("cross-tenant response exposed security metadata: %s", otherRes.Body.String())
	}

	ownerReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/messages/%d", message.ID), nil)
	ownerReq = ownerReq.WithContext(context.WithValue(ownerReq.Context(), userContextKey, currentUser{User: owner}))
	ownerRes := httptest.NewRecorder()
	server.apiMessage(ownerRes, ownerReq, message.ID)
	if ownerRes.Code != http.StatusOK {
		t.Fatalf("owner message status = %d body=%s", ownerRes.Code, ownerRes.Body.String())
	}
	var payload struct {
		Thread []apiThreadMessage `json:"thread"`
	}
	if err := json.NewDecoder(ownerRes.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Thread) != 1 || payload.Thread[0].SecurityIndicators == nil {
		t.Fatalf("thread security indicators = %+v", payload.Thread)
	}
	security := payload.Thread[0].SecurityIndicators
	if security.ReportedAuthentication == nil {
		t.Fatalf("reported authentication missing: %+v", security)
	}
	assertAPIReportedAuthentication(t, security.ReportedAuthentication.SPF, "fail", "authentication-results")
	assertAPIReportedAuthentication(t, security.ReportedAuthentication.DKIM, "pass", "authentication-results")
	assertAPIReportedAuthentication(t, security.ReportedAuthentication.DMARC, "fail", "authentication-results")
	if len(security.Signals) < 3 {
		t.Fatalf("security signals = %+v", security.Signals)
	}
}

func assertReportedAuthentication(t *testing.T, got *reportedAuthenticationResult, result, source string) {
	t.Helper()
	if got == nil || got.Result != result || got.Source != source {
		t.Fatalf("reported authentication = %+v, want %s from %s", got, result, source)
	}
}

func assertAPIReportedAuthentication(t *testing.T, got *apiReportedAuthenticationResult, result, source string) {
	t.Helper()
	if got == nil || got.Result != result || got.Source != source {
		t.Fatalf("API reported authentication = %+v, want %s from %s", got, result, source)
	}
}

func assertSecuritySignal(t *testing.T, signals []messageSecuritySignal, kind, displayHost, targetHost, scheme string) {
	t.Helper()
	if !securitySignalPresent(signals, kind, displayHost, targetHost, scheme) {
		t.Fatalf("missing signal %s/%s/%s/%s in %+v", kind, displayHost, targetHost, scheme, signals)
	}
}

func securitySignalPresent(signals []messageSecuritySignal, kind, displayHost, targetHost, scheme string) bool {
	for _, signal := range signals {
		if signal.Kind == kind && signal.DisplayHost == displayHost && signal.TargetHost == targetHost && signal.Scheme == scheme {
			return true
		}
	}
	return false
}
