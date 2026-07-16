package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestAPIAccountFolderSearchIndexRebuildIsTenantScopedAndUsesMailboxMaintenance(t *testing.T) {
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
	blobStore := blob.New(dir)

	owner, err := db.CreateUser(ctx, "rebuild-owner@example.test", "Rebuild Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "rebuild-other@example.test", "Rebuild Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccount, ownerMailbox, ownerMessage := createSearchRebuildMessage(t, ctx, db, blobStore, owner, "Gmail Forward", 101, "canonicalrebuildneedle")
	_, otherMailbox, otherMessage := createSearchRebuildMessage(t, ctx, db, blobStore, other, "Private", 202, "othertenantrebuildneedle")

	corruptOwnerDocument := ownerMessage
	corruptOwnerDocument.Subject = "obsoleteindexneedle"
	corruptOwnerDocument.BodyText = "obsoleteindexneedle"
	if err := searchService.IndexMessage(ctx, corruptOwnerDocument, nil); err != nil {
		t.Fatal(err)
	}
	otherDocument := otherMessage
	otherDocument.Subject = "othertenantrebuildneedle"
	otherDocument.BodyText = "othertenantrebuildneedle"
	if err := searchService.IndexMessage(ctx, otherDocument, nil); err != nil {
		t.Fatal(err)
	}

	runnerContext, cancelRunner := context.WithCancel(context.Background())
	defer cancelRunner()
	syncService := &syncer.Service{Store: db, Blobs: blobStore, Search: searchService}
	runner := syncer.NewRunnerWithContext(runnerContext, syncService)
	server := &Server{
		store: db, blobs: blobStore, search: searchService, syncer: syncService, syncRunner: runner,
		masterKey: bytes.Repeat([]byte{7}, 32), events: newEventHub(),
	}

	foreignResponse := httptest.NewRecorder()
	server.handleAPI(foreignResponse, authenticatedFolderActionRequest(t, server, owner,
		fmt.Sprintf("/api/account/folders/%d/search-index/rebuild", otherMailbox.ID)))
	if foreignResponse.Code != http.StatusNotFound {
		t.Fatalf("foreign mailbox rebuild status=%d body=%s", foreignResponse.Code, foreignResponse.Body.String())
	}

	rebuildResponse := httptest.NewRecorder()
	server.handleAPI(rebuildResponse, authenticatedFolderActionRequest(t, server, owner,
		fmt.Sprintf("/api/account/folders/%d/search-index/rebuild", ownerMailbox.ID)))
	if rebuildResponse.Code != http.StatusOK {
		t.Fatalf("rebuild status=%d body=%s", rebuildResponse.Code, rebuildResponse.Body.String())
	}
	var queued struct {
		OK     bool  `json:"ok"`
		Queued bool  `json:"queued"`
		RunID  int64 `json:"run_id"`
	}
	if err := json.NewDecoder(rebuildResponse.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.OK || !queued.Queued || queued.RunID <= 0 {
		t.Fatalf("rebuild response=%+v", queued)
	}
	run := waitForSearchRebuildRun(t, ctx, db, owner.ID, queued.RunID)
	if run.Status != "ok" || run.AccountID != ownerAccount.ID || run.CurrentMailbox != ownerMailbox.Name {
		t.Fatalf("rebuild run=%+v", run)
	}
	if run.LatestNewSubject != "Rebuilding full-text index" || run.MessagesStored != 1 || run.MessagesSeen != 2 || run.MessagesTotal != 2 {
		t.Fatalf("rebuild progress=%+v", run)
	}

	assertSearchMessageIDs(t, ctx, searchService, owner.ID, "canonicalrebuildneedle", []int64{ownerMessage.ID})
	assertSearchMessageIDs(t, ctx, searchService, owner.ID, "obsoleteindexneedle", nil)
	assertSearchMessageIDs(t, ctx, searchService, other.ID, "othertenantrebuildneedle", []int64{otherMessage.ID})

	started := make(chan struct{})
	release := make(chan struct{})
	blockingRun, maintenanceStarted, err := runner.StartMailboxMaintenance(owner.ID, ownerMailbox, "Blocking maintenance", func(ctx context.Context, _ int64, _ *store.SyncProgress) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil || !maintenanceStarted {
		t.Fatalf("start blocking maintenance started=%t err=%v", maintenanceStarted, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking maintenance did not start")
	}
	conflictResponse := httptest.NewRecorder()
	server.handleAPI(conflictResponse, authenticatedFolderActionRequest(t, server, owner,
		fmt.Sprintf("/api/account/folders/%d/search-index/rebuild", ownerMailbox.ID)))
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("reserved mailbox rebuild status=%d body=%s", conflictResponse.Code, conflictResponse.Body.String())
	}
	close(release)
	if run := waitForSearchRebuildRun(t, ctx, db, owner.ID, blockingRun.ID); run.Status != "ok" {
		t.Fatalf("blocking maintenance run=%+v", run)
	}

	disabledMailbox, err := db.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "Search Disabled")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSettings(ctx, owner.ID, disabledMailbox.ID, store.MailboxSettings{
		SyncMode: disabledMailbox.SyncMode, Role: disabledMailbox.Role, Icon: disabledMailbox.Icon,
		ShowInSidebar: disabledMailbox.ShowInSidebar, ShowInAllMail: disabledMailbox.ShowInAllMail, IncludeInSearch: false,
	}); err != nil {
		t.Fatal(err)
	}
	disabledResponse := httptest.NewRecorder()
	server.handleAPI(disabledResponse, authenticatedFolderActionRequest(t, server, owner,
		fmt.Sprintf("/api/account/folders/%d/search-index/rebuild", disabledMailbox.ID)))
	if disabledResponse.Code != http.StatusBadRequest {
		t.Fatalf("search-disabled mailbox rebuild status=%d body=%s", disabledResponse.Code, disabledResponse.Body.String())
	}
	waitForSearchRebuildNotifications(t, server, owner.ID)
}

func createSearchRebuildMessage(t *testing.T, ctx context.Context, db *store.Store, blobStore *blob.Store, user store.User, mailboxName string, uid uint32, needle string) (store.MailAccount, store.Mailbox, store.MessageRecord) {
	t.Helper()
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, mailboxName)
	if err != nil {
		t.Fatal(err)
	}
	if !mailbox.IncludeInSearch {
		t.Fatalf("test mailbox %q unexpectedly excluded from search", mailboxName)
	}
	raw := []byte(fmt.Sprintf("From: Sender <sender@example.test>\r\nTo: %s\r\nSubject: Rebuild %s\r\nMessage-ID: <%d-%d@example.test>\r\nDate: Wed, 15 Jul 2026 12:00:00 -0600\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", user.Email, needle, user.ID, uid, needle))
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, uid, raw)
	if err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blobRecord.ID,
		MessageIDHeader: fmt.Sprintf("<%d-%d@example.test>", user.ID, uid), Subject: "Stored subject",
		FromAddr: "Sender <sender@example.test>", ToAddr: user.Email, Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: uid, Size: saved.Size, BlobPath: saved.Path, BodyText: "stored preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	return account, mailbox, message
}

func authenticatedFolderActionRequest(t *testing.T, server *Server, user store.User, target string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, nil)
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, currentUser{User: user}))
	const csrfBase = "folder-action-csrf"
	request.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfBase})
	request.Header.Set("X-CSRF-Token", server.csrfForBase(csrfBase))
	return request
}

func waitForSearchRebuildRun(t *testing.T, ctx context.Context, db *store.Store, userID, runID int64) store.SyncRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := db.GetSyncRunForUser(ctx, userID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "running" {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("rebuild run %d did not finish: %+v", runID, run)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertSearchMessageIDs(t *testing.T, ctx context.Context, service *search.Service, userID int64, query string, want []int64) {
	t.Helper()
	got, err := service.Search(ctx, userID, query, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("search user=%d query=%q ids=%v, want %v", userID, query, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("search user=%d query=%q ids=%v, want %v", userID, query, got, want)
		}
	}
}

func waitForSearchRebuildNotifications(t *testing.T, server *Server, userID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.webPushScheduleMu.Lock()
		running := server.webPushRunning[userID]
		server.webPushScheduleMu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("search rebuild notification worker did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
