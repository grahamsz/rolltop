// File overview: Tests for server setup, ETags, storage stats, and route behavior.

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestWriteJSONCachedETagNotModified(t *testing.T) {
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mail", nil)
	writeJSONCached(first, req, map[string]any{"ok": true, "count": 2})
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing etag")
	}

	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/mail", nil)
	req.Header.Set("If-None-Match", etag)
	writeJSONCached(second, req, map[string]any{"ok": true, "count": 2})
	if second.Code != http.StatusNotModified {
		t.Fatalf("second status = %d", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 body = %q", second.Body.String())
	}
}

func TestImmutableFrontendAssetCacheScope(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "assets/index-release123.js", want: true},
		{path: "assets/index-release123.css", want: true},
		{path: "assets/chunk-release123.JS", want: true},
		{path: "sw.js", want: false},
		{path: "manifest.webmanifest", want: false},
		{path: "index.html", want: false},
		{path: "icon.svg", want: false},
	}
	for _, tt := range cases {
		if got := isImmutableFrontendAsset(tt.path); got != tt.want {
			t.Fatalf("isImmutableFrontendAsset(%q) = %t, want %t", tt.path, got, tt.want)
		}
	}
}

func TestHandleAppServesContactsRoute(t *testing.T) {
	dir := t.TempDir()
	distDir := filepath.Join(dir, frontendDistDir)
	if err := os.MkdirAll(distDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte("<!doctype html><html><body>contacts</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	server := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/contacts", nil)

	server.handleApp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("cache-control = %q, want private, no-store", got)
	}
	if got := rec.Header().Get("Vary"); got != "*" {
		t.Fatalf("vary = %q, want *", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "contacts") {
		t.Fatalf("body = %q", body)
	}
}

func TestHandleAppEmbedsOnlyCurrentUserBootstrap(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	foreign, err := db.CreateUser(ctx, "foreign-startup@example.test", "Foreign Startup", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "owner-startup@example.test", "Owner Startup", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	distDir := filepath.Join(dir, frontendDistDir)
	if err := os.MkdirAll(distDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte(`<!doctype html><html><head><meta name="rolltop-startup" /></head><body><div id="root"></div><script type="module" src="/assets/index.js"></script></body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	server := &Server{store: db, startedAt: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/mail", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: owner}))
	rec := httptest.NewRecorder()
	server.handleApp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), foreign.Email) || strings.Contains(rec.Body.String(), foreign.Name) {
		t.Fatalf("startup HTML exposed foreign user: %s", rec.Body.String())
	}
	payload := startupPayloadFromHTML(t, rec.Body.String())
	user, ok := payload["user"].(map[string]any)
	userID, idOK := user["id"].(float64)
	if !ok || !idOK || user["email"] != owner.Email || int64(userID) != owner.ID {
		t.Fatalf("startup user = %#v", payload["user"])
	}

	anon := httptest.NewRecorder()
	server.handleApp(anon, httptest.NewRequest(http.MethodGet, "/mail", nil))
	anonPayload := startupPayloadFromHTML(t, anon.Body.String())
	if anonPayload["user"] != nil || strings.Contains(anon.Body.String(), owner.Email) || strings.Contains(anon.Body.String(), foreign.Email) {
		t.Fatalf("anonymous startup exposed a user: %#v", anonPayload["user"])
	}
}

func TestInjectStartupBootstrapEscapesScriptTerminator(t *testing.T) {
	index := []byte(`<html><head><meta name="rolltop-startup" /></head><body><div id="root"></div></body></html>`)
	name := `Owner </script><script>alert("x")</script>`
	injected, err := injectStartupBootstrap(index, map[string]any{"user": map[string]any{"name": name}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(injected), name) || strings.Contains(string(injected), `</script><script>`) {
		t.Fatalf("startup JSON was not HTML escaped: %s", injected)
	}
	payload := startupPayloadFromHTML(t, string(injected))
	user := payload["user"].(map[string]any)
	if user["name"] != name {
		t.Fatalf("decoded name = %q, want %q", user["name"], name)
	}
	if _, err := injectStartupBootstrap([]byte(`<html><body><div id="root"></div></body></html>`), map[string]any{}); err == nil {
		t.Fatal("missing startup marker should fail")
	}
}

func startupPayloadFromHTML(t *testing.T, html string) map[string]any {
	t.Helper()
	const start = `<script id="rolltop-startup" type="application/json">`
	startIndex := strings.Index(html, start)
	if startIndex < 0 {
		t.Fatalf("missing startup payload in %s", html)
	}
	rest := html[startIndex+len(start):]
	endIndex := strings.Index(rest, `</script>`)
	if endIndex < 0 {
		t.Fatalf("unterminated startup payload in %s", html)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rest[:endIndex]), &payload); err != nil {
		t.Fatalf("decode startup payload: %v", err)
	}
	return payload
}

func TestFrontendAssetCacheControl(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{path: "assets/index-release123.js", want: immutableFrontendAssetCacheControl},
		{path: "assets/index-release123.css", want: immutableFrontendAssetCacheControl},
		{path: "sw.js", want: "no-cache"},
		{path: "manifest.webmanifest", want: ""},
		{path: "icon.svg", want: ""},
	}
	for _, tt := range cases {
		if got := frontendAssetCacheControl(tt.path); got != tt.want {
			t.Fatalf("frontendAssetCacheControl(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestAndroidLatestMetadataUsesRequestHost(t *testing.T) {
	dir := t.TempDir()
	androidDir := filepath.Join(dir, frontendDistDir, "android")
	if err := os.MkdirAll(androidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(androidDir, "latest.json"), []byte(`{"versionCode":2,"versionName":"0.2.0","apkUrl":"/android/rolltop.apk","sha256":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(androidDir, "rolltop.apk"), []byte("apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	server := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/android/latest.json", nil)
	req.Host = "mail.example.test"
	req.Header.Set("X-Forwarded-Proto", "https")

	server.handleAndroidLatest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got androidLatestMetadata
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.APKURL != "https://mail.example.test/android/rolltop.apk" {
		t.Fatalf("apkUrl = %q", got.APKURL)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("metadata cache-control = %q", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/android/rolltop.apk", nil)
	server.handleAndroidAPK(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apk status = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.android.package-archive" {
		t.Fatalf("apk content-type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="rolltop.apk"` {
		t.Fatalf("apk content-disposition = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("apk cache-control = %q", got)
	}
	if rec.Body.String() != "apk" {
		t.Fatalf("apk body = %q", rec.Body.String())
	}
}

func TestAPIMailCachedETagShortCircuitsBeforeStore(t *testing.T) {
	user := store.User{ID: 42, Email: "cache@example.test", Name: "Cache"}
	server := &Server{mailListCache: newMailListCache()}
	key := mailListCacheKey{UserID: user.ID, MailboxID: 7, Page: 3}
	etag := `"cached-mailbox-page"`
	server.rememberMailListETag(key, etag, server.mailListGeneration(user.ID))

	req := httptest.NewRequest(http.MethodGet, "/api/mail?mailbox=7&page=3", nil)
	req.Header.Set("If-None-Match", etag)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiMail(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("etag = %q, want %q", got, etag)
	}
}

func TestAPIMailFirstPageServesWarmMemoryCache(t *testing.T) {
	user := store.User{ID: 42, Email: "cache@example.test", Name: "Cache"}
	server := &Server{mailListCache: newMailListCache()}
	body, etag, err := cachedJSONBody(map[string]any{
		"conversations":  []apiConversation{},
		"page":           1,
		"has_prev":       false,
		"has_next":       false,
		"active_mailbox": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := mailListCacheKey{UserID: user.ID, Page: 1}
	server.rememberMailListPage(key, etag, body, server.mailListGeneration(user.ID))

	req := httptest.NewRequest(http.MethodGet, "/api/mail?page=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiMail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("etag = %q, want %q", got, etag)
	}
	if got := rec.Header().Get("X-Rolltop-Mail-Stats"); got != "cache=memory" {
		t.Fatalf("mail stats = %q", got)
	}
	if rec.Body.String() != string(body) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), string(body))
	}
}

func TestWarmMailFirstPageInvalidatesOnUserChange(t *testing.T) {
	userID := int64(42)
	server := &Server{mailListCache: newMailListCache()}
	body, etag, err := cachedJSONBody(map[string]any{"page": 1})
	if err != nil {
		t.Fatal(err)
	}
	key := mailListCacheKey{UserID: userID, Page: 1}
	server.rememberMailListPage(key, etag, body, server.mailListGeneration(userID))
	if _, ok := server.mailListCache.page(key); !ok {
		t.Fatal("warm page was not cached")
	}
	server.noteMailListChanged(userID)
	if _, ok := server.mailListCache.page(key); ok {
		t.Fatal("warm page remained cached after user change")
	}
}

func TestAPISearchCachedETagShortCircuitsBeforeSearch(t *testing.T) {
	user := store.User{ID: 43, Email: "search-cache.test", Name: "Search Cache"}
	server := &Server{mailListCache: newMailListCache()}
	key := mailListCacheKey{UserID: user.ID, Page: 2, Search: true, Query: "needle"}
	etag := `"cached-search-page"`
	server.rememberMailListETag(key, etag, server.mailListGeneration(user.ID))

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=needle&page=2", nil)
	req.Header.Set("If-None-Match", etag)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiSearch(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("etag = %q, want %q", got, etag)
	}
	if got := rec.Header().Get("Server-Timing"); !strings.Contains(got, "cache") {
		t.Fatalf("server timing = %q", got)
	}
	if got := rec.Header().Get("X-Rolltop-Search-Stats"); got != "cache=hit" {
		t.Fatalf("rolltop search stats = %q", got)
	}
}

func TestAPISearchWritesTimingHeaders(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	user, err := db.CreateUser(ctx, "timing@example.test", "Timing", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, search: searchSvc, mailListCache: newMailListCache()}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=needle&page=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	serverTiming := rec.Header().Get("Server-Timing")
	for _, part := range []string{"filter;dur=", "sender;dur=", "bleve;dur=", "hydrate;dur=", "render;dur=", "total;dur="} {
		if !strings.Contains(serverTiming, part) {
			t.Fatalf("server timing %q missing %q", serverTiming, part)
		}
	}
	stats := rec.Header().Get("X-Rolltop-Search-Stats")
	for _, part := range []string{"cache=miss", "page=1", "batches=1", "raw_hits=0", "seeds=0"} {
		if !strings.Contains(stats, part) {
			t.Fatalf("search stats %q missing %q", stats, part)
		}
	}
	if strings.Contains(serverTiming, "needle") || strings.Contains(stats, "needle") {
		t.Fatalf("search headers leaked query: timing=%q stats=%q", serverTiming, stats)
	}
}

func TestAPISearchRepairsRecentMissingSearchDocument(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()

	user, err := db.CreateUser(ctx, "nick-search@example.test", "Nick Search", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993, Username: user.Email, EncryptedPassword: "encrypted", Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: "messages/nick.eml", SHA256: "nick-sha", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:    user.ID,
		AccountID: account.ID,
		MailboxID: mailbox.ID,
		BlobID:    blob.ID,
		Subject:   "Checking In",
		FromAddr:  `"Nick Koncilja" <nick@riverrise.com>`,
		ToAddr:    user.Email,
		Date:      time.Now().UTC(),
		UID:       101,
		BodyText:  "All good. nbk Nick Koncilja",
	})
	if err != nil {
		t.Fatal(err)
	}
	if indexed, err := searchSvc.MessageIDsIndexed(ctx, user.ID, []int64{msg.ID}); err != nil || indexed[msg.ID] {
		t.Fatalf("message should start missing from Bleve indexed=%v err=%v", indexed, err)
	}

	server := &Server{store: db, search: searchSvc, mailListCache: newMailListCache()}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=nick", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiSearch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Conversations []apiConversation `json:"conversations"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Conversations) == 0 || payload.Conversations[0].Message.ID != msg.ID {
		t.Fatalf("conversations = %#v", payload.Conversations)
	}
	indexed, err := searchSvc.MessageIDsIndexed(ctx, user.ID, []int64{msg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !indexed[msg.ID] {
		t.Fatal("expected search request to repair missing Bleve document")
	}
}

func TestAPIMessageSearchExplanationRepairsAndPrefersClickedMessage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()

	user, err := db.CreateUser(ctx, "thread-search@example.test", "Thread Search", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993, Username: user.Email, EncryptedPassword: "encrypted", Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	threadKey := "thread-nick-explanation"
	base := time.Now().UTC()
	firstBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: "messages/nick-first.eml", SHA256: "nick-first-sha", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	clickedBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: "messages/nick-clicked.eml", SHA256: "nick-clicked-sha", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	thirdBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: "messages/nick-third.eml", SHA256: "nick-third-sha", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: firstBlob.ID, ThreadKey: threadKey,
		Subject: "Checking In", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, ToAddr: user.Email, Date: base, UID: 201, BodyText: "Hey Graham, checking in. Nick",
	})
	if err != nil {
		t.Fatal(err)
	}
	clicked, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: clickedBlob.ID, ThreadKey: threadKey,
		Subject: "Re: Checking In", FromAddr: `"Graham Stewart" <graham@example.test>`, ToAddr: `"Nick Koncilja" <nick@riverrise.com>`, Date: base.Add(6 * time.Minute), UID: 202, BodyText: "I sent the check.",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: thirdBlob.ID, ThreadKey: threadKey,
		Subject: "Re: Checking In", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, ToAddr: user.Email, Date: base.Add(25 * time.Minute), UID: 203, BodyText: "All good.",
	})
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{store: db, search: searchSvc, mailListCache: newMailListCache()}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/messages/%d/search-explanation?q=nick&hit=%d", clicked.ID, first.ID), nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.apiMessageSearchExplanation(rec, req, clicked.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Matched            bool     `json:"matched"`
		MessageID          int64    `json:"message_id"`
		RequestedMessageID int64    `json:"requested_message_id"`
		Fields             []string `json:"fields"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Matched || payload.MessageID != clicked.ID || payload.RequestedMessageID != clicked.ID {
		t.Fatalf("payload = %#v", payload)
	}
	if !slices.Contains(payload.Fields, "to") {
		t.Fatalf("fields = %v, want to", payload.Fields)
	}
}

func TestMailListCachedETagInvalidatesOnUserChange(t *testing.T) {
	userID := int64(99)
	server := &Server{events: newEventHub(), mailListCache: newMailListCache()}
	key := mailListCacheKey{UserID: userID, Page: 1}
	etag := `"cached-all-mail"`
	server.rememberMailListETag(key, etag, server.mailListGeneration(userID))

	req := httptest.NewRequest(http.MethodGet, "/api/mail?page=1", nil)
	req.Header.Set("If-None-Match", etag)
	if !server.writeMailListNotModifiedIfFresh(httptest.NewRecorder(), req, key) {
		t.Fatal("expected cached etag to be fresh before invalidation")
	}

	server.notifyUserChanged(userID)

	if server.writeMailListNotModifiedIfFresh(httptest.NewRecorder(), req, key) {
		t.Fatal("expected cached etag to be stale after invalidation")
	}
}

func TestSyncProgressNotificationDoesNotInvalidateMailList(t *testing.T) {
	userID := int64(100)
	server := &Server{events: newEventHub(), mailListCache: newMailListCache()}
	key := mailListCacheKey{UserID: userID, Page: 1}
	etag := `"cached-progress"`
	server.rememberMailListETag(key, etag, server.mailListGeneration(userID))
	changed, unsubscribe := server.events.Subscribe(userID)
	defer unsubscribe()

	server.notifySyncProgress(userID)

	select {
	case <-changed:
	default:
		t.Fatal("sync progress did not notify the active SSE subscriber")
	}
	if got := server.mailListGeneration(userID); got != 0 {
		t.Fatalf("sync progress advanced mail-list generation to %d", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/mail?page=1", nil)
	req.Header.Set("If-None-Match", etag)
	if !server.writeMailListNotModifiedIfFresh(httptest.NewRecorder(), req, key) {
		t.Fatal("sync progress invalidated an unchanged cached mail page")
	}
}

func TestCreateMailIdentityEndpointCreatesMeIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "identity-api@example.test", "Identity API", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "alias-api@example.test", Host: "imap.alias-api.test", Port: 993, Username: "alias-api@example.test", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSettings(ctx, user.ID, sent.ID, store.MailboxSettings{SyncMode: sent.SyncMode, Role: "sent", Icon: sent.Icon, ShowInSidebar: true, ShowInAllMail: sent.ShowInAllMail, IncludeInSearch: true}); err != nil {
		t.Fatal(err)
	}
	sent, err = db.GetMailboxForUser(ctx, user.ID, sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	drafts, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Drafts")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSettings(ctx, user.ID, drafts.ID, store.MailboxSettings{SyncMode: drafts.SyncMode, Role: "drafts", Icon: drafts.Icon, ShowInSidebar: true, ShowInAllMail: drafts.ShowInAllMail, IncludeInSearch: true}); err != nil {
		t.Fatal(err)
	}
	drafts, err = db.GetMailboxForUser(ctx, user.ID, drafts.ID)
	if err != nil {
		t.Fatal(err)
	}
	smtp, err := db.CreateSMTPAccount(ctx, store.SMTPAccount{UserID: user.ID, Label: "Alias API SMTP", Host: "smtp.alias-api.test", Port: 587, Username: "alias-api@example.test", EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db}
	body := bytes.NewBufferString(fmt.Sprintf(`{"email":"alias-api@example.test","display_name":"Alias API","smtp_account_id":%d,"imap_account_id":%d,"sent_mailbox_id":%d,"drafts_mailbox_id":%d,"signature":"<p>Alias API</p>","is_primary":true}`, smtp.ID, account.ID, sent.ID, drafts.ID))
	req := httptest.NewRequest(http.MethodPost, "/api/account/identities", body)
	req.Header.Set("Content-Type", "application/json")
	csrfBase := "identity-create-csrf-base"
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfBase})
	req.Header.Set("X-CSRF-Token", server.csrfForBase(csrfBase))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.handleAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST identity status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Identity   apiMailIdentity   `json:"identity"`
		Identities []apiMailIdentity `json:"identities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Identity.ID == 0 || payload.Identity.Email != "alias-api@example.test" || payload.Identity.DisplayName != "Alias API" || payload.Identity.SMTPAccountID != smtp.ID || payload.Identity.IMAPAccountID != account.ID || payload.Identity.SentMailboxID != sent.ID || payload.Identity.DraftsMailboxID != drafts.ID {
		t.Fatalf("identity response = %+v", payload.Identity)
	}
	if len(payload.Identities) != 1 {
		t.Fatalf("identities response = %+v", payload.Identities)
	}
	contacts, err := db.ListMeContactsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || !contacts[0].IsMe || len(contacts[0].Emails) != 1 || contacts[0].Emails[0].Email != "alias-api@example.test" {
		t.Fatalf("me contacts after identity create = %+v", contacts)
	}
}

func TestPGPPrivateKeyAPIAutocryptDefaultsOnForFirstIdentityKey(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "pgp-api@example.test", "PGP API", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := db.CreateMailIdentityForUser(ctx, user.ID, store.MailIdentity{
		Email:            "pgp-api@example.test",
		DisplayName:      "PGP API",
		AutocryptEnabled: false,
		IsPrimary:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.AutocryptEnabled {
		t.Fatal("test setup expected Autocrypt disabled")
	}
	manifests, backendPlugins := testClientSidePGPBackendPlugins(t)
	server := &Server{store: db, masterKey: []byte("12345678901234567890123456789012"), pluginManifests: manifests, backendPlugins: backendPlugins}
	body := bytes.NewBufferString(fmt.Sprintf(`{
		"identity_id":%d,
		"label":"PGP API",
		"fingerprint":"00112233445566778899AABBCCDDEEFF00112233",
		"key_id":"CCDDEEFF00112233",
		"user_ids":"PGP API <pgp-api@example.test>",
		"public_key_armored":"-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nx\n-----END PGP PUBLIC KEY BLOCK-----",
		"private_key_storage":"browser",
		"is_active_signing":true,
		"is_active_encryption":true
	}`, identity.ID))
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/client_side_pgp/private-keys", body)
	req.Header.Set("Content-Type", "application/json")
	csrfBase := "pgp-key-csrf-base"
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfBase})
	req.Header.Set("X-CSRF-Token", server.csrfForBase(csrfBase))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.handleAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST PGP key status = %d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := db.GetMailIdentityForUser(ctx, user.ID, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.AutocryptEnabled {
		t.Fatal("first active identity PGP key did not enable Autocrypt by default")
	}
}

func TestDeleteSMTPAccountEndpointUnlinksIdentities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "smtp-api@example.test", "SMTP API", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	smtp, err := db.CreateSMTPAccount(ctx, store.SMTPAccount{UserID: user.ID, Label: "API SMTP", Host: "smtp.api.test", Port: 587, Username: user.Email, EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, user.ID, store.Contact{DisplayName: "SMTP API", IsMe: true, IsPrimary: true, Emails: []store.ContactEmail{{Email: user.Email, IsPrimary: true}}}); err != nil {
		t.Fatal(err)
	}
	if identities, err := db.ListMailIdentitiesForUser(ctx, user.ID); err != nil || len(identities) != 1 || identities[0].SMTPAccountID != smtp.ID {
		t.Fatalf("identities before delete = %+v err=%v", identities, err)
	}
	server := &Server{store: db, masterKey: []byte("12345678901234567890123456789012")}
	csrfBase := "smtp-delete-csrf-base"
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/account/smtp/%d", smtp.ID), nil)
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfBase})
	req.Header.Set("X-CSRF-Token", server.csrfForBase(csrfBase))
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: user}))
	rec := httptest.NewRecorder()

	server.handleAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE smtp status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := db.GetSMTPAccountForUser(ctx, user.ID, smtp.ID); !store.IsNotFound(err) {
		t.Fatalf("deleted smtp lookup err = %v, want not found", err)
	}
	identities, err := db.ListMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].SMTPAccountID != 0 {
		t.Fatalf("identities after delete = %+v, want default smtp", identities)
	}
}

func TestSetupCreatesFirstAdmin(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(Options{
		Store:      db,
		MasterKey:  []byte("12345678901234567890123456789012"),
		SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET /api/bootstrap status = %d", get.Code)
	}
	if got := get.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("GET /api/bootstrap cache-control = %q", got)
	}
	if got := get.Header().Get("X-rolltop-Version"); got == "" {
		t.Fatal("missing X-rolltop-Version header")
	}
	if got := get.Header().Get("Link"); !strings.Contains(got, "https://rolltop.app") {
		t.Fatalf("Link header = %q, want rolltop.app", got)
	}
	var bootstrap struct {
		UsersExist bool   `json:"users_exist"`
		CSRF       string `json:"csrf"`
	}
	if err := json.NewDecoder(get.Body).Decode(&bootstrap); err != nil {
		t.Fatal(err)
	}
	if bootstrap.UsersExist {
		t.Fatal("users should not exist before setup")
	}
	if bootstrap.CSRF == "" {
		t.Fatal("missing csrf token")
	}
	var anonCookie *http.Cookie
	for _, c := range get.Result().Cookies() {
		if c.Name == csrfCookie {
			anonCookie = c
			break
		}
	}
	if anonCookie == nil {
		t.Fatal("missing csrf cookie")
	}

	body := []byte(`{"email":"admin@example.test","name":"Admin","password":"correct horse battery staple"}`)
	post := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", bootstrap.CSRF)
	req.AddCookie(anonCookie)
	handler.ServeHTTP(post, req)
	if post.Code != http.StatusOK {
		t.Fatalf("POST /api/setup status = %d body=%s", post.Code, post.Body.String())
	}
	count, err := db.CountUsers(req.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("users = %d", count)
	}
	user, err := db.GetUserByEmail(req.Context(), "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	identities, err := db.ListMailIdentitiesForUser(req.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].Email != "admin@example.test" || !identities[0].IsPrimary {
		t.Fatalf("setup identities = %+v", identities)
	}
	var sessionSet bool
	for _, c := range post.Result().Cookies() {
		if c.Name == sessionCookie && c.HttpOnly && c.Value != "" {
			sessionSet = true
		}
	}
	if !sessionSet {
		t.Fatal("session cookie was not set")
	}
}

func TestStorageStatsReportsCurrentUserOnly(t *testing.T) {
	dir := t.TempDir()
	writeStorageFile := func(path string, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeStorageFile(filepath.Join(dir, "rolltop.db"), "server database should not count")
	writeStorageFile(filepath.Join(dir, "bleve", "index"), "server index should not count")
	writeStorageFile(filepath.Join(dir, "blobs", "server"), "server blobs should not count")
	writeStorageFile(filepath.Join(dir, "users", "1", "rolltop.db"), "database")
	writeStorageFile(filepath.Join(dir, "users", "1", "bleve", "index"), "bleve")
	writeStorageFile(filepath.Join(dir, "users", "1", "bleve", "store", "001.zap"), "zap-one")
	writeStorageFile(filepath.Join(dir, "users", "1", "bleve", "store", "002.zap"), "largest-zap")
	writeStorageFile(filepath.Join(dir, "users", "1", "bleve", "store", "root.bolt"), "root")
	writeStorageFile(filepath.Join(dir, "users", "1", "blobs", "blob"), "blobdata")
	writeStorageFile(filepath.Join(dir, "users", "2", "rolltop.db"), "other database should not count for user one")
	writeStorageFile(filepath.Join(dir, "users", "2", "bleve", "index"), "other index should not count for user one")
	writeStorageFile(filepath.Join(dir, "users", "2", "blobs", "blob"), "other blob should not count for user one")

	server := &Server{dataDir: dir, databasePath: filepath.Join(dir, "rolltop.db"), indexPath: filepath.Join(dir, "bleve")}
	stats := server.cachedStorageStats(1)
	if stats.DatabaseBytes != 8 {
		t.Fatalf("database bytes = %d", stats.DatabaseBytes)
	}
	if stats.IndexBytes != 27 {
		t.Fatalf("index bytes = %d", stats.IndexBytes)
	}
	if stats.IndexBreakdown.FileCount != 4 {
		t.Fatalf("index file count = %d", stats.IndexBreakdown.FileCount)
	}
	if stats.IndexBreakdown.ZapCount != 2 || stats.IndexBreakdown.ZapBytes != 18 {
		t.Fatalf("zap breakdown = %+v", stats.IndexBreakdown)
	}
	if stats.IndexBreakdown.LargestZapBytes != 11 || stats.IndexBreakdown.LargestZapPath != "store/002.zap" {
		t.Fatalf("largest zap = %+v", stats.IndexBreakdown)
	}
	if stats.IndexBreakdown.RootBytes != 4 || stats.IndexBreakdown.OtherBytes != 5 {
		t.Fatalf("root/other breakdown = %+v", stats.IndexBreakdown)
	}
	if stats.BlobBytes != 8 {
		t.Fatalf("blob bytes = %d", stats.BlobBytes)
	}
	if stats.TotalBytes != 43 {
		t.Fatalf("total bytes = %d", stats.TotalBytes)
	}
	if strings.Contains(stats.DatabasePath, "users/2") || strings.Contains(stats.IndexPath, "users/2") || strings.Contains(stats.BlobPath, "users/2") {
		t.Fatalf("storage paths include another user: %+v", stats)
	}

	other := server.cachedStorageStats(2)
	if other.DatabaseBytes == stats.DatabaseBytes && other.IndexBytes == stats.IndexBytes && other.BlobBytes == stats.BlobBytes {
		t.Fatalf("per-user storage cache returned same stats for different users: user1=%+v user2=%+v", stats, other)
	}
}

func TestSyncFolderViewsUsesCommittedSearchMarkers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()

	user, err := db.CreateUser(ctx, "search-stats@example.test", "Search Stats", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             "search-stats@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          "search-stats@example.test",
		EncryptedPassword: "encrypted",
		Mailbox:           "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 4, 0, 5, 0); err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: "messages/search-stats.eml", SHA256: "sha", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:    user.ID,
		AccountID: account.ID,
		MailboxID: mailbox.ID,
		BlobID:    blob.ID,
		Subject:   "Indexed",
		Date:      time.Now(),
		UID:       1,
		BodyText:  "indexed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:    user.ID,
		AccountID: account.ID,
		MailboxID: mailbox.ID,
		BlobID:    blob.ID,
		Subject:   "Missing",
		Date:      time.Now(),
		UID:       2,
		BodyText:  "missing",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageAttachmentIndexed(ctx, user.ID, first.ID, false); err != nil {
		t.Fatal(err)
	}
	// Simulate an old-generation document whose SQLite row was purged. Settings
	// progress intentionally reads only the durable SQLite commit marker; exact
	// missing/stale document reconciliation belongs to background index repair.
	stale := first
	stale.ID += 1_000_000
	stale.UID += 1_000_000
	stale.Subject = "Stale generation"
	if err := searchSvc.IndexMessage(ctx, stale, nil); err != nil {
		t.Fatal(err)
	}
	if count, err := searchSvc.CountMailboxMessages(ctx, user.ID, mailbox.ID); err != nil || count != 1 {
		t.Fatalf("raw search index count = %d, %v; want 1, nil", count, err)
	}
	if _, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Empty"); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: db, search: searchSvc}
	views, err := server.syncFolderViews(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %d", len(views))
	}
	var box, emptyBox store.MailboxSummary
	var foundBox, foundEmpty bool
	for _, view := range views {
		switch view.Mailbox.Name {
		case "INBOX":
			box = view.Mailbox
			foundBox = true
		case "Empty":
			emptyBox = view.Mailbox
			foundEmpty = true
		}
	}
	if !foundBox || !foundEmpty {
		t.Fatalf("missing expected mailbox views: inbox=%t empty=%t", foundBox, foundEmpty)
	}
	if box.LocalMessageCount != 2 {
		t.Fatalf("local message count = %d", box.LocalMessageCount)
	}
	if box.RemoteMessageCount != 4 {
		t.Fatalf("remote message count = %d", box.RemoteMessageCount)
	}
	if box.SearchIndexedCount == nil || *box.SearchIndexedCount != 1 {
		t.Fatalf("search indexed count = %v", box.SearchIndexedCount)
	}
	if box.SearchIndexTotal == nil || *box.SearchIndexTotal != 2 {
		t.Fatalf("search index total = %v", box.SearchIndexTotal)
	}
	if box.SearchIndexPercent == nil || *box.SearchIndexPercent != 50 {
		t.Fatalf("search index percent = %v", box.SearchIndexPercent)
	}
	if emptyBox.SearchIndexPercent == nil || *emptyBox.SearchIndexPercent != 0 {
		t.Fatalf("empty search index percent = %v", emptyBox.SearchIndexPercent)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE mailboxes SET search_index_state_known = 0 WHERE user_id = ? AND id = ?`, user.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}
	views, err = server.syncFolderViews(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Mailbox.ID == mailbox.ID {
			if view.Mailbox.SearchIndexKnown || view.Mailbox.SearchIndexedCount != nil || view.Mailbox.SearchIndexPercent != nil {
				t.Fatalf("unverified search progress = known:%t indexed:%v percent:%v",
					view.Mailbox.SearchIndexKnown, view.Mailbox.SearchIndexedCount, view.Mailbox.SearchIndexPercent)
			}
			return
		}
	}
	t.Fatal("unverified mailbox view was not returned")
}

func TestSyncFolderViewsUsesUnboundedTenantScopedMailboxHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "folder-history@example.test", "Folder History", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccount, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: owner.ID, Email: owner.Email, Host: "imap.example.test", Port: 993,
		Username: owner.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "Archive"); err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-folder-history@example.test", "Other Folder History", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: other.ID, Email: other.Email, Host: "imap.other.test", Port: 993,
		Username: other.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertRun := func(userID, accountID int64, mailbox string, updated int64) int64 {
		t.Helper()
		result, insertErr := db.DB().ExecContext(ctx, `INSERT INTO sync_runs
			(user_id, account_id, status, started_at, finished_at, updated_at, current_mailbox, current_uid)
			VALUES (?, ?, 'ok', ?, ?, ?, ?, ?)`, userID, accountID, updated, updated, updated, mailbox, updated)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		id, insertErr := result.LastInsertId()
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		return id
	}

	archiveRunID := insertRun(owner.ID, ownerAccount.ID, "Archive", 1)
	var latestInboxRunID int64
	for i := int64(0); i < 401; i++ {
		latestInboxRunID = insertRun(owner.ID, ownerAccount.ID, "INBOX", 100+i)
	}
	otherArchiveRunID := insertRun(other.ID, otherAccount.ID, "Archive", 10_000)
	recent, err := db.ListSyncRunsForUser(ctx, owner.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range recent {
		if run.ID == archiveRunID {
			t.Fatalf("test premise failed: Archive run %d remained in bounded recent history", archiveRunID)
		}
	}

	views, err := (&Server{store: db}).syncFolderViews(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("folder views = %+v, want INBOX and Archive", views)
	}
	for _, view := range views {
		if view.LastRun == nil {
			t.Fatalf("folder %q has no last run after complete-history lookup", view.Mailbox.Name)
		}
		if view.LastRun.UserID != owner.ID {
			t.Fatalf("folder %q exposed user %d to owner %d", view.Mailbox.Name, view.LastRun.UserID, owner.ID)
		}
		switch strings.ToLower(strings.TrimSpace(view.Mailbox.Name)) {
		case "archive":
			if view.LastRun.ID != archiveRunID {
				t.Fatalf("Archive last run = %d, want owner run %d (other user run %d)", view.LastRun.ID, archiveRunID, otherArchiveRunID)
			}
		case "inbox":
			if view.LastRun.ID != latestInboxRunID {
				t.Fatalf("INBOX last run = %d, want %d", view.LastRun.ID, latestInboxRunID)
			}
		}
	}
}

func TestMoveRefreshMailboxNamesIncludesSourceAndDestination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUser(ctx, "move@example.test", "Move", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             "move@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          "move@example.test",
		EncryptedPassword: "encrypted",
		Mailbox:           "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "ManualSource")
	if err != nil {
		t.Fatal(err)
	}
	dest, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Trash")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "raw", Path: "messages/move.eml", SHA256: "sha", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:    user.ID,
		AccountID: account.ID,
		MailboxID: source.ID,
		BlobID:    blob.ID,
		Subject:   "Move me",
		Date:      time.Now(),
		UID:       42,
		BodyText:  "body",
	})
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{store: db}
	names, err := server.moveRefreshMailboxNames(ctx, user.ID, []int64{msg.ID}, dest)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ManualSource", "Trash"}
	if len(names) != len(want) {
		t.Fatalf("names = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}
