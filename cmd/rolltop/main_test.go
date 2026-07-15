package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rolltop/backend/store"
)

func TestListenStartupHTTPReturnsBindFailureImmediately(t *testing.T) {
	bindErr := errors.New("bind failed")
	listener, err := listenStartupHTTPWith(func(network, address string) (net.Listener, error) {
		if network != "tcp" || address != ":8080" {
			t.Fatalf("listen called with %q, %q", network, address)
		}
		return nil, bindErr
	}, ":8080")
	if listener != nil {
		_ = listener.Close()
		t.Fatal("listener unexpectedly returned after bind failure")
	}
	if !errors.Is(err, bindErr) {
		t.Fatalf("bind error = %v, want wrapped bind failure", err)
	}
}

func TestStartupGateServesStartupHTMLForAppRoutes(t *testing.T) {
	gate := &startupGate{state: newStartupState()}
	req := httptest.NewRequest(http.MethodGet, "/mailbox/97/p3", nil)
	res := httptest.NewRecorder()

	gate.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if !strings.Contains(res.Body.String(), "rolltop") {
		t.Fatalf("startup body did not contain rolltop branding")
	}
}

func TestStartupGateKeepsAPIUnavailableUntilReady(t *testing.T) {
	gate := &startupGate{state: newStartupState()}
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	res := httptest.NewRecorder()

	gate.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusServiceUnavailable)
	}
}

func TestStartupHTMLShowsFailureMessage(t *testing.T) {
	state := newStartupState()
	state.fail(errors.New("ROLLTOP_MASTER_KEY is required"))
	rec := httptest.NewRecorder()

	writeStartupHTML(rec, state.snapshotCopy())

	body := rec.Body.String()
	if !strings.Contains(body, "Startup failed") {
		t.Fatalf("startup body did not contain failure phase")
	}
	if !strings.Contains(body, "ROLLTOP_MASTER_KEY is required") {
		t.Fatalf("startup body did not contain startup error")
	}
}

func TestAppRuntimeCloseStopsPluginHost(t *testing.T) {
	closer := &runtimeTestCloser{}
	app := &appRuntime{pluginHost: closer}

	app.close()

	if closer.calls != 1 {
		t.Fatalf("plugin host Close calls = %d, want 1", closer.calls)
	}
}

type runtimeTestCloser struct {
	calls int
}

func (c *runtimeTestCloser) Close() error {
	c.calls++
	return nil
}

func TestInboxAutoTargetsIncludesEveryAccountInbox(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "idle@example.test", "Idle", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "first@example.test", Host: "imap.first.test", Port: 993, Username: "first", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "second@example.test", Host: "imap.second.test", Port: 993, Username: "second", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}

	targets, err := inboxAutoTargets(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, target := range targets {
		if target.UserID == user.ID && target.Mailbox.Name == "INBOX" {
			seen[target.Account.ID] = true
		}
	}
	if !seen[first.ID] || !seen[second.ID] || len(seen) != 2 {
		t.Fatalf("targets = %+v, want both account inboxes", targets)
	}
}
