package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"rolltop/backend/auth"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
	"rolltop/internal/testlog"
)

func newLoginSecurityTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hash, err := auth.HashPasswordWithParams("correct-password", auth.Argon2idParams{
		Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"owner@example.test", "other@example.test"} {
		if _, err := db.CreateUser(context.Background(), email, "User", hash, false); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Options{
		Store: db, MasterKey: []byte("12345678901234567890123456789012"),
		PluginDir: t.TempDir(), DisableBackgroundWorkers: true,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Use the same low-cost hash for timing equalization in these tests.
	s.dummyVerifyOnce.Do(func() { s.dummyHash = hash })
	return s
}

func loginSecurityRequest(s *Server, email, password, remote, forwarded string) *http.Request {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	r := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", forwarded)
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "private-csrf-base"})
	r.Header.Set("X-CSRF-Token", s.csrfForBase("private-csrf-base"))
	return r
}

func TestLoginFailedSecurityLogs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		email      string
		remote     string
		forwarded  string
		noThrottle bool
		wantIP     string
	}{
		{name: "wrong password", email: "owner@example.test", remote: "10.0.0.1:1234", forwarded: "192.0.2.99, 198.51.100.1", wantIP: "198.51.100.1"},
		{name: "unknown account", email: "unknown@example.test", remote: "10.0.0.1:1234", forwarded: "198.51.100.1", wantIP: "198.51.100.1"},
		{name: "untrusted peer", email: "owner@example.test", remote: "198.51.100.2:1234", forwarded: "192.0.2.99", wantIP: "198.51.100.2"},
		{name: "malformed header", email: "owner@example.test", remote: "10.0.0.1:1234", forwarded: "198.51.100.1\nsecurity login_failed ip=192.0.2.99", wantIP: "10.0.0.1"},
		{name: "without throttle", email: "owner@example.test", remote: "10.0.0.1:1234", forwarded: "198.51.100.1", noThrottle: true, wantIP: "198.51.100.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newLoginSecurityTestServer(t)
			if tc.noThrottle {
				s.loginThrottle = nil
			}
			logs := testlog.Capture(t)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, loginSecurityRequest(s, tc.email, "secret-wrong-password", tc.remote, tc.forwarded))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			// Exact matching also excludes email, password, cookies, and raw
			// headers from the log and prevents injected extra records.
			if want := "security login_failed ip=" + tc.wantIP + "\n"; logs.String() != want {
				t.Fatalf("log = %q, want %q", logs.String(), want)
			}
			if s.loginThrottle != nil && len(s.loginThrottle.byIP[tc.wantIP]) != 1 {
				t.Fatalf("failure not recorded for resolved client IP: %v", s.loginThrottle.byIP)
			}
		})
	}
}

func TestLoginProxyIPThrottleAndSessionIsolation(t *testing.T) {
	s := newLoginSecurityTestServer(t)
	logs := testlog.Capture(t)
	for i := 0; i < loginMaxFailuresPerIP; i++ {
		s.loginThrottle.recordFailure("attacker@example.test", "198.51.100.1")
	}
	// Client-controlled entries cannot evade the IP flood cap.
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loginSecurityRequest(s, "owner@example.test", "correct-password", "10.0.0.1:1234", "192.0.2.99, 198.51.100.1"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("flooding client status = %d, want 429", rec.Code)
	}
	if got := logs.String(); got != "security login_throttled ip=198.51.100.1\n" {
		t.Fatalf("throttle log = %q", got)
	}
	logs.Reset()

	// Another client behind the same proxy can still sign in. A successful
	// login clears only that account's failure state and creates its session.
	s.loginThrottle.recordFailure("owner@example.test", "198.51.100.2")
	s.loginThrottle.recordFailure("other@example.test", "198.51.100.2")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loginSecurityRequest(s, "owner@example.test", "correct-password", "10.0.0.1:1234", "198.51.100.2"))
	if rec.Code != http.StatusOK {
		t.Fatalf("other client status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if logs.Len() != 0 {
		t.Fatalf("successful login emitted failure log: %q", logs.String())
	}
	if s.loginThrottle.byEmail["owner@example.test"] != nil || s.loginThrottle.byEmail["other@example.test"] == nil {
		t.Fatal("successful login did not clear only its own account's failures")
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookie {
			_, user, err := s.store.GetSessionUser(context.Background(), mmcrypto.TokenHash(cookie.Value))
			if err != nil || user.Email != "owner@example.test" {
				t.Fatalf("session belongs to %q, err = %v", user.Email, err)
			}
			return
		}
	}
	t.Fatal("successful login did not issue a session cookie")
}

func TestLoginAccountThrottleStillAppliesAcrossProxyClients(t *testing.T) {
	s := newLoginSecurityTestServer(t)
	for i := 0; i < loginMaxFailuresPerEmail; i++ {
		s.loginThrottle.recordFailure("owner@example.test", "198.51.100.1")
	}
	logs := testlog.Capture(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loginSecurityRequest(s, "owner@example.test", "correct-password", "10.0.0.1:1234", "198.51.100.2"))
	if rec.Code != http.StatusTooManyRequests || logs.String() != "security login_throttled ip=198.51.100.2\n" {
		t.Fatalf("locked account: status = %d, log = %q", rec.Code, logs.String())
	}
}

func TestLoginInvalidRequestsDoNotLogCredentialFailures(t *testing.T) {
	s := newLoginSecurityTestServer(t)
	for _, invalid := range []string{"csrf", "json"} {
		t.Run(invalid, func(t *testing.T) {
			logs := testlog.Capture(t)
			r := loginSecurityRequest(s, "owner@example.test", "secret-wrong-password", "10.0.0.1:1234", "198.51.100.1")
			wantStatus := http.StatusForbidden
			if invalid == "csrf" {
				r.Header.Del("X-CSRF-Token")
			} else {
				r.Body = http.NoBody
				wantStatus = http.StatusBadRequest
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, r)
			if rec.Code != wantStatus || strings.Contains(logs.String(), "security login_") {
				t.Fatalf("invalid request: status = %d, log = %q", rec.Code, logs.String())
			}
		})
	}
}
