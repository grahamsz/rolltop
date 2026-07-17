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

func TestAPIAccountFolderSearchVisibilityUsesMailboxMaintenance(t *testing.T) {
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

	user, err := db.CreateUser(ctx, "visibility-owner@example.test", "Visibility Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, mailbox, message := createSearchRebuildMessage(t, ctx, db, blobStore, user, "Receipts", 301, "visibilityreconcileneedle")
	if err := db.UpdateMailboxSettings(ctx, user.ID, mailbox.ID, mailboxSettingsForSearchVisibility(mailbox, false)); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}

	runnerContext, cancelRunner := context.WithCancel(context.Background())
	defer cancelRunner()
	syncService := &syncer.Service{Store: db, Blobs: blobStore, Search: searchService}
	runner := syncer.NewRunnerWithContext(runnerContext, syncService)
	server := &Server{
		store: db, blobs: blobStore, search: searchService, syncer: syncService, syncRunner: runner,
		masterKey: bytes.Repeat([]byte{8}, 32), events: newEventHub(),
	}

	started := make(chan struct{})
	release := make(chan struct{})
	blockingRun, ok, err := runner.StartMailboxMaintenance(user.ID, mailbox, "Blocking maintenance", func(ctx context.Context, _ int64, _ *store.SyncProgress) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil || !ok {
		t.Fatalf("start blocking maintenance ok=%t err=%v", ok, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking maintenance did not start")
	}

	conflict := httptest.NewRecorder()
	server.handleAPI(conflict, authenticatedFolderSettingsRequest(t, server, user, mailbox, true))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("reserved mailbox settings status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	stored, err := db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.IncludeInSearch {
		t.Fatal("conflicting visibility save mutated include_in_search before obtaining the mailbox reservation")
	}

	close(release)
	if run := waitForSearchRebuildRun(t, ctx, db, user.ID, blockingRun.ID); run.Status != "ok" {
		t.Fatalf("blocking maintenance run=%+v", run)
	}
	waitForAccountMailboxIdle(t, runner, user.ID, mailbox)

	enable := httptest.NewRecorder()
	server.handleAPI(enable, authenticatedFolderSettingsRequest(t, server, user, mailbox, true))
	if enable.Code != http.StatusOK {
		t.Fatalf("enable search status=%d body=%s", enable.Code, enable.Body.String())
	}
	var queued struct {
		OK     bool  `json:"ok"`
		Queued bool  `json:"queued"`
		RunID  int64 `json:"run_id"`
	}
	if err := json.NewDecoder(enable.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.OK || !queued.Queued || queued.RunID <= 0 {
		t.Fatalf("enable search response=%+v", queued)
	}
	run := waitForSearchRebuildRun(t, ctx, db, user.ID, queued.RunID)
	if run.Status != "ok" || run.AccountID != account.ID || run.CurrentMailbox != mailbox.Name || run.LatestNewSubject != "Enabling full-text search" {
		t.Fatalf("visibility reconciliation run=%+v", run)
	}
	waitForAccountMailboxIdle(t, runner, user.ID, mailbox)
	stored, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var summary store.MailboxSummary
	for _, candidate := range summaries {
		if candidate.ID == mailbox.ID {
			summary = candidate
			break
		}
	}
	if !stored.IncludeInSearch || summary.SearchIndexPurged || !summary.SearchIndexKnown {
		t.Fatalf("mailbox after visibility reconciliation mailbox=%+v summary=%+v", stored, summary)
	}
	counts, err := db.CountSearchIndexedMessagesByMailbox(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if counts[mailbox.ID] != 1 {
		t.Fatalf("search-indexed count=%d, want 1", counts[mailbox.ID])
	}
	assertSearchMessageIDs(t, ctx, searchService, user.ID, "visibilityreconcileneedle", []int64{message.ID})
}

func mailboxSettingsForSearchVisibility(mailbox store.Mailbox, include bool) store.MailboxSettings {
	return store.MailboxSettings{
		SyncMode: mailbox.SyncMode, Role: mailbox.Role, Icon: mailbox.Icon,
		ShowInSidebar: mailbox.ShowInSidebar, ShowInAllMail: mailbox.ShowInAllMail, IncludeInSearch: include,
	}
}

func authenticatedFolderSettingsRequest(t *testing.T, server *Server, user store.User, mailbox store.Mailbox, include bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"sync_mode": mailbox.SyncMode, "role": mailbox.Role, "icon": mailbox.Icon,
		"show_in_sidebar": mailbox.ShowInSidebar, "show_in_all_mail": mailbox.ShowInAllMail, "include_in_search": include,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/account/folders/%d/settings", mailbox.ID), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, currentUser{User: user}))
	const csrfBase = "folder-settings-csrf"
	request.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrfBase})
	request.Header.Set("X-CSRF-Token", server.csrfForBase(csrfBase))
	return request
}

func waitForAccountMailboxIdle(t *testing.T, runner *syncer.Runner, userID int64, mailbox store.Mailbox) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runner.IsAccountMailboxRunning(userID, mailbox.AccountID, mailbox.Name) {
		if time.Now().After(deadline) {
			t.Fatalf("mailbox reservation did not clear user_id=%d account_id=%d mailbox=%q", userID, mailbox.AccountID, mailbox.Name)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
