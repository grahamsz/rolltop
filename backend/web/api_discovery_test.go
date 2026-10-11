package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/maildiscovery"
	"rolltop/backend/store"
	"rolltop/internal/testlog"
)

type discoveryFunc func(context.Context, string) (maildiscovery.Result, error)

func (f discoveryFunc) Discover(ctx context.Context, email string) (maildiscovery.Result, error) {
	return f(ctx, email)
}

func discoveryRequest(s *Server, cookie *http.Cookie, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/account/discover", strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", s.csrfForBase(cookie.Value))
	}
	return r
}

func TestMailDiscoveryRequiresAuthAndCSRFAndOnlyAcceptsEmail(t *testing.T) {
	db, s, handler, cookie := newStoreFailureTestServer(t)
	defer db.Close()
	s.mailDiscovery = discoveryFunc(func(context.Context, string) (maildiscovery.Result, error) {
		t.Fatal("unauthorized discovery lookup")
		return maildiscovery.Result{}, nil
	})
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"anonymous", `{"email":"alice@example.com"}`, 401},
		{"csrf", `{"email":"alice@example.com"}`, 403},
		{"user ID", `{"email":"alice@example.com","user_id":2}`, 400},
		{"password", `{"email":"alice@example.com","password":"secret"}`, 400},
		{"trailing JSON", `{"email":"alice@example.com"}{}`, 400},
		{"oversized", `{"email":"` + strings.Repeat("a", 1024) + `@example.com"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := discoveryRequest(s, cookie, tc.body)
			if tc.name == "anonymous" {
				r.Header.Del("Cookie")
			}
			if tc.name == "csrf" {
				r.Header.Del("X-CSRF-Token")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMailDiscoveryIsPrivateAndDoesNotSaveAccounts(t *testing.T) {
	db, s, handler, cookie := newStoreFailureTestServer(t)
	defer db.Close()
	other, err := db.CreateUser(context.Background(), "other@example.com", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(context.Background(), other.ID, mmcrypto.TokenHash("other-session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	logs := testlog.Capture(t)
	s.mailDiscovery = discoveryFunc(func(_ context.Context, email string) (maildiscovery.Result, error) {
		return maildiscovery.Result{IMAP: &maildiscovery.Server{Host: "imap.example.com", Port: 993, Username: email, Security: "tls", Source: "Provider autoconfig"}}, nil
	})
	for _, tc := range []struct {
		cookie *http.Cookie
		email  string
	}{
		{cookie, "first@example.com"}, {&http.Cookie{Name: sessionCookie, Value: "other-session"}, "second@example.com"},
	} {
		r := discoveryRequest(s, tc.cookie, `{"email":"`+tc.email+`"}`)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status = %d, headers = %v", rec.Code, rec.Header())
		}
		var got maildiscovery.Result
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.IMAP == nil || got.IMAP.Username != tc.email || got.SMTP != nil {
			t.Fatalf("cross-request discovery data: %s", rec.Body.String())
		}
	}
	users, err := db.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		accounts, err := db.ListMailAccountsForUser(context.Background(), user.ID)
		if err != nil || len(accounts) != 0 {
			t.Fatalf("discovery saved accounts for user %d", user.ID)
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("discovery logged private data: %q", logs.String())
	}
}

func TestDiscoveredSMTPSettingsSurviveOnboarding(t *testing.T) {
	for _, tc := range []struct {
		name, smtpHost string
		derive         bool
		wantHost       string
	}{
		{"different SMTP host", "submission.provider.example", false, "submission.provider.example"},
		{"shared IMAP hostname", "imap.example.com", false, "imap.example.com"},
		{"legacy inferred SMTP", "imap.example.com", true, "smtp.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, s, handler, cookie := newStoreFailureTestServer(t)
			defer db.Close()
			foreign, err := db.CreateUser(context.Background(), "foreign@example.com", "Foreign", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.CreateSMTPAccount(context.Background(), store.SMTPAccount{UserID: foreign.ID, Host: "foreign.example.com", Port: 465, Username: "foreign", EncryptedPassword: "foreign-password", UseTLS: true}); err != nil {
				t.Fatal(err)
			}
			in := accountSettingsInput{Email: "alice@example.com", Host: "imap.example.com", Port: 993, Username: "alice", Password: "secret-mail-password", UseTLS: true, SMTPHost: tc.smtpHost, SMTPPort: 465, SMTPUsername: "submission-user", SMTPUseTLS: true, SMTPSameAsIMAP: tc.derive}
			body, _ := json.Marshal(in)
			r := discoveryRequest(s, cookie, string(body))
			r.URL.Path = "/api/account/imap"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
			}
			_, owner, err := db.GetSessionUser(context.Background(), mmcrypto.TokenHash(cookie.Value))
			if err != nil {
				t.Fatal(err)
			}
			accounts, err := db.ListSMTPAccountsForUser(context.Background(), owner.ID)
			if err != nil || len(accounts) != 1 {
				t.Fatalf("SMTP accounts = %+v, %v", accounts, err)
			}
			if accounts[0].Host != tc.wantHost || accounts[0].Port != 465 || !accounts[0].UseTLS {
				t.Fatalf("discovered SMTP settings lost: %+v", accounts[0])
			}
			if !tc.derive && accounts[0].Username != "submission-user" {
				t.Fatal("discovered username overwritten")
			}
			password, err := mmcrypto.DecryptString(s.masterKey, accounts[0].EncryptedPassword)
			if err != nil || password != in.Password {
				t.Fatal("IMAP password was not encrypted and reused for SMTP")
			}
			others, err := db.ListSMTPAccountsForUser(context.Background(), foreign.ID)
			if err != nil || len(others) != 1 || others[0].Host != "foreign.example.com" {
				t.Fatal("other user's SMTP settings changed")
			}
		})
	}
}
