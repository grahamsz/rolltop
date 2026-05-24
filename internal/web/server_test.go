package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mailmirror/internal/store"
)

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

func TestStorageStatsReportsSQLiteBleveAndBlobs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mailmirror.db")
	indexPath := filepath.Join(dir, "bleve")
	blobRoot := filepath.Join(dir, "blobs")
	if err := os.MkdirAll(indexPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(blobRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexPath, "index"), []byte("bleve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobRoot, "blob"), []byte("blobdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{dataDir: dir, databasePath: dbPath, indexPath: indexPath}
	stats := server.storageStats()
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
