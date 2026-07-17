package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestAPIAccountFolderProgressIsLightweightAndTenantScoped(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()

	owner, err := db.CreateUser(ctx, "progress@example.test", "Progress", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "progress-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccount, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: owner.ID, Email: owner.Email, Host: "imap.example.test", Port: 993,
		Username: owner.Email, EncryptedPassword: "owner-secret", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: other.ID, Email: other.Email, Host: "imap.example.test", Port: 993,
		Username: other.Email, EncryptedPassword: "other-secret", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerMailbox, err := db.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	createCommittedMessage := func(user store.User, account store.MailAccount, mailbox store.Mailbox, sha string) {
		t.Helper()
		blob, createErr := db.CreateBlob(ctx, store.BlobRecord{
			UserID: user.ID, Kind: "message", Path: "messages/" + sha + ".eml", SHA256: sha, Size: 1,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		message, createErr := db.CreateMessage(ctx, store.CreateMessage{
			UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
			UID: 1, Date: time.Now(), InternalDate: time.Now(), Subject: "progress",
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if createErr := db.MarkMessageAttachmentIndexed(ctx, user.ID, message.ID, false); createErr != nil {
			t.Fatal(createErr)
		}
	}
	createCommittedMessage(owner, ownerAccount, ownerMailbox, "owner")
	createCommittedMessage(other, otherAccount, otherMailbox, "other")

	server := &Server{store: db, search: searchService}
	req := httptest.NewRequest(http.MethodGet, "/api/account/folders/progress", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, currentUser{User: owner}))
	rec := httptest.NewRecorder()
	server.handleAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("folder progress status = %d body = %s", rec.Code, rec.Body.String())
	}
	if timing := rec.Header().Get("Server-Timing"); !strings.Contains(timing, "folders;dur=") {
		t.Fatalf("Server-Timing = %q", timing)
	}
	var payload struct {
		Folders []apiFolderProgress `json:"folders"`
	}
	body := append([]byte(nil), rec.Body.Bytes()...)
	if strings.Contains(string(body), `"last_run"`) {
		t.Fatalf("compact folder progress included sync-run history: %s", body)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Folders) != 1 {
		t.Fatalf("folder progress = %+v, want one owner folder", payload.Folders)
	}
	progress := payload.Folders[0]
	if progress.MailboxID != ownerMailbox.ID {
		t.Fatalf("mailbox id = %d, want owner mailbox %d (other %d)", progress.MailboxID, ownerMailbox.ID, otherMailbox.ID)
	}
	// Bleve is deliberately empty. A count of one proves the request used the
	// post-commit SQLite marker rather than opening/enumerating the index.
	if progress.SearchIndexedCount == nil || *progress.SearchIndexedCount != 1 {
		t.Fatalf("search indexed count = %v, want committed marker count 1", progress.SearchIndexedCount)
	}
	if progress.SearchIndexPurged {
		t.Fatal("healthy mailbox reported an explicit search purge")
	}
	if !progress.SearchIndexKnown {
		t.Fatal("fresh healthy mailbox reported unverified search state")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || raw["folders"] == nil {
		t.Fatalf("folder progress leaked full account graph keys: %v", raw)
	}
	if err := db.MarkMailboxSearchIndexPurged(ctx, owner.ID, ownerMailbox.ID); err != nil {
		t.Fatal(err)
	}
	purgedReq := httptest.NewRequest(http.MethodGet, "/api/account/folders/progress", nil)
	purgedReq = purgedReq.WithContext(context.WithValue(purgedReq.Context(), userContextKey, currentUser{User: owner}))
	purgedRec := httptest.NewRecorder()
	server.handleAPI(purgedRec, purgedReq)
	if purgedRec.Code != http.StatusOK {
		t.Fatalf("purged folder progress status = %d body = %s", purgedRec.Code, purgedRec.Body.String())
	}
	payload.Folders = nil
	if err := json.Unmarshal(purgedRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Folders) != 1 || !payload.Folders[0].SearchIndexPurged || !payload.Folders[0].SearchIndexKnown ||
		payload.Folders[0].SearchIndexedCount == nil || *payload.Folders[0].SearchIndexedCount != 0 {
		t.Fatalf("purged folder progress = %+v, want explicit purge with zero indexed", payload.Folders)
	}

	unauthenticated := httptest.NewRecorder()
	server.handleAPI(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/account/folders/progress", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", unauthenticated.Code, http.StatusUnauthorized)
	}
	post := httptest.NewRequest(http.MethodPost, "/api/account/folders/progress", nil)
	post = post.WithContext(context.WithValue(post.Context(), userContextKey, currentUser{User: owner}))
	methodNotAllowedResponse := httptest.NewRecorder()
	server.handleAPI(methodNotAllowedResponse, post)
	if methodNotAllowedResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", methodNotAllowedResponse.Code, http.StatusMethodNotAllowed)
	}
}
