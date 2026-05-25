// File overview: Tests for server setup, ETags, storage stats, and route behavior.

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailmirror/backend/search"
	"mailmirror/backend/store"
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

func TestSetupCreatesFirstAdmin(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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

	writeStorageFile(filepath.Join(dir, "mailmirror.db"), "server database should not count")
	writeStorageFile(filepath.Join(dir, "bleve", "index"), "server index should not count")
	writeStorageFile(filepath.Join(dir, "blobs", "server"), "server blobs should not count")
	writeStorageFile(filepath.Join(dir, "users", "1", "mailmirror.db"), "database")
	writeStorageFile(filepath.Join(dir, "users", "1", "bleve", "index"), "bleve")
	writeStorageFile(filepath.Join(dir, "users", "1", "blobs", "blob"), "blobdata")
	writeStorageFile(filepath.Join(dir, "users", "2", "mailmirror.db"), "other database should not count for user one")
	writeStorageFile(filepath.Join(dir, "users", "2", "bleve", "index"), "other index should not count for user one")
	writeStorageFile(filepath.Join(dir, "users", "2", "blobs", "blob"), "other blob should not count for user one")

	server := &Server{dataDir: dir, databasePath: filepath.Join(dir, "mailmirror.db"), indexPath: filepath.Join(dir, "bleve")}
	stats := server.cachedStorageStats(1)
	if stats.DatabaseBytes != 8 {
		t.Fatalf("database bytes = %d", stats.DatabaseBytes)
	}
	if stats.IndexBytes != 5 {
		t.Fatalf("index bytes = %d", stats.IndexBytes)
	}
	if stats.BlobBytes != 8 {
		t.Fatalf("blob bytes = %d", stats.BlobBytes)
	}
	if stats.TotalBytes != 21 {
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

func TestSyncFolderViewsIncludesSearchIndexStats(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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
	if err := searchSvc.IndexMessage(ctx, first, nil); err != nil {
		t.Fatal(err)
	}

	server := &Server{store: db, search: searchSvc}
	views := server.syncFolderViews(ctx, user.ID, nil)
	if len(views) != 1 {
		t.Fatalf("views = %d", len(views))
	}
	box := views[0].Mailbox
	if box.LocalMessageCount != 2 {
		t.Fatalf("local message count = %d", box.LocalMessageCount)
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
}

func TestMoveRefreshMailboxNamesIncludesSourceAndDestination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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
