package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestAPIAccountFolderSearchIndexRebuildRequiresBleve(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-no-bleve@example.test", "No Bleve", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Gmail Forward")
	if err != nil {
		t.Fatal(err)
	}
	runnerContext, cancelRunner := context.WithCancel(context.Background())
	defer cancelRunner()
	service := &syncer.Service{Store: db}
	server := &Server{
		store: db, syncer: service, syncRunner: syncer.NewRunnerWithContext(runnerContext, service),
		masterKey: bytes.Repeat([]byte{3}, 32),
	}

	response := httptest.NewRecorder()
	server.handleAPI(response, authenticatedFolderActionRequest(t, server, user,
		fmt.Sprintf("/api/account/folders/%d/search-index/rebuild", mailbox.ID)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("rebuild without Bleve status=%d body=%s", response.Code, response.Body.String())
	}
	runs, err := db.ListSyncRunsForUser(ctx, user.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("rebuild without Bleve created runs=%+v", runs)
	}
}

func TestAPIAccountSearchIndexRebuildCoversOnlyOwnedSearchableFolders(t *testing.T) {
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
	owner, err := db.CreateUser(ctx, "account-rebuild@example.test", "Account Rebuild", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-account-rebuild@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, inbox, inboxMessage := createSearchRebuildMessage(t, ctx, db, blobStore, owner, "INBOX", 1, "account-rebuild-inbox")
	archive, err := db.GetOrCreateMailbox(ctx, owner.ID, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	archiveMessage := createLocalSearchRebuildMessage(t, ctx, db, blobStore, owner, account, archive, 2)
	otherAccount, otherMailbox, otherMessage := createSearchRebuildMessage(t, ctx, db, blobStore, other, "INBOX", 1, "account-rebuild-other")
	for _, message := range []store.MessageRecord{inboxMessage, archiveMessage, otherMessage} {
		if err := searchService.IndexMessage(ctx, message, nil); err != nil {
			t.Fatal(err)
		}
	}
	runnerContext, cancelRunner := context.WithCancel(context.Background())
	defer cancelRunner()
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchService}
	server := &Server{
		store: db, blobs: blobStore, search: searchService, syncer: service,
		syncRunner: syncer.NewRunnerWithContext(runnerContext, service),
		masterKey:  bytes.Repeat([]byte{6}, 32), events: newEventHub(),
	}

	response := httptest.NewRecorder()
	server.handleAPI(response, authenticatedFolderActionRequest(t, server, owner,
		fmt.Sprintf("/api/account/imap/%d/rebuild-search-index", account.ID)))
	if response.Code != http.StatusOK {
		t.Fatalf("account rebuild status=%d body=%s", response.Code, response.Body.String())
	}
	var queued struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	run := waitForSearchRebuildRun(t, ctx, db, owner.ID, queued.RunID)
	if run.Status != "ok" || run.MailboxesDone != 2 || run.MailboxesTotal != 2 {
		t.Fatalf("account rebuild run=%+v", run)
	}
	for _, mailbox := range []store.Mailbox{inbox, archive} {
		count, err := searchService.CountMailboxMessages(ctx, owner.ID, mailbox.ID)
		if err != nil || count != 1 {
			t.Fatalf("owner mailbox %q index count=%d err=%v", mailbox.Name, count, err)
		}
	}
	otherCount, err := searchService.CountMailboxMessages(ctx, other.ID, otherMailbox.ID)
	if err != nil || otherCount != 1 {
		t.Fatalf("other user's mailbox count=%d err=%v", otherCount, err)
	}

	response = httptest.NewRecorder()
	server.handleAPI(response, authenticatedFolderActionRequest(t, server, owner,
		fmt.Sprintf("/api/account/imap/%d/rebuild-search-index", otherAccount.ID)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-user account rebuild status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAPIAccountFolderSearchIndexRebuildFinishesAcrossGenerationRecovery(t *testing.T) {
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
	user, err := db.CreateUser(ctx, "rebuild-retry@example.test", "Rebuild Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	account, mailbox, message := createSearchRebuildMessage(t, ctx, db, blobStore, user, "Gmail Forward", 1, "local-rebuild-1")
	if err := searchService.IndexMessage(ctx, message, nil); err != nil {
		t.Fatal(err)
	}
	for uid := uint32(2); uid <= 25; uid++ {
		message = createLocalSearchRebuildMessage(t, ctx, db, blobStore, user, account, mailbox, uid)
		if err := searchService.IndexMessage(ctx, message, nil); err != nil {
			t.Fatal(err)
		}
	}
	const uidValidity = uint32(91)
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 26, 0, 27, uidValidity); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	remoteBlob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: "remote/rebuild-retry-26.eml", SHA256: "remote-rebuild-retry-26",
	})
	if err != nil {
		t.Fatal(err)
	}
	blockedMessage, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: remoteBlob.ID,
		MessageIDHeader: "<rebuild-retry-26@example.test>", Subject: "Blocked rebuild message",
		FromAddr: "sender@example.test", ToAddr: user.Email, Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: 26, UIDValidity: int64(uidValidity), Size: 128, BodyText: "blocked-rebuild-preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := searchService.IndexMessage(ctx, blockedMessage, nil); err != nil {
		t.Fatal(err)
	}

	fetcher := &interruptOnceSearchRebuildFetcher{
		blockUID:    blockedMessage.UID,
		uidValidity: uidValidity,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		raw:         []byte("From: Sender <sender@example.test>\r\nTo: rebuild-retry@example.test\r\nSubject: Retried full message\r\nMessage-ID: <rebuild-retry-26@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nretried-full-body-needle\r\n"),
	}
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchService, Fetcher: fetcher}
	firstRunnerContext, cancelFirstRunner := context.WithCancel(context.Background())
	defer cancelFirstRunner()
	firstRunner := syncer.NewRunnerWithContext(firstRunnerContext, service)
	server := &Server{
		store: db, blobs: blobStore, search: searchService, syncer: service, syncRunner: firstRunner,
		masterKey: bytes.Repeat([]byte{5}, 32), events: newEventHub(),
	}

	firstRunID := startSearchRebuildRequest(t, server, user, mailbox.ID)
	select {
	case <-fetcher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("repair did not reach the blocking raw message")
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	nowUnix := time.Now().UTC().Unix()
	if _, err := userDB.ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, arrival_uid_floor, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?)`, user.ID, account.ID, mailbox.ID, uidValidity+1, nowUnix, nowUnix); err != nil {
		t.Fatal(err)
	}
	firstRunner.SignalMailboxGenerationRecovery(user.ID)
	// Explicit rebuilds use a committed runner context, so generation recovery
	// must wait for this requested task instead of cancelling it midway.
	close(fetcher.release)
	firstRun := waitForSearchRebuildRun(t, ctx, db, user.ID, firstRunID)
	if firstRun.Status != "ok" {
		t.Fatalf("rebuild run=%+v", firstRun)
	}
	rebuiltCount, err := searchService.CountMailboxMessages(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltCount != 26 {
		t.Fatalf("rebuilt documents=%d, want 26", rebuiltCount)
	}
	assertSearchMessageIDs(t, ctx, searchService, user.ID, "retried-full-body-needle", []int64{blockedMessage.ID})
	if _, err := userDB.ExecContext(ctx, `DELETE FROM mailbox_generation_rebuilds
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, user.ID, account.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}
	waitForSearchRebuildNotifications(t, server, user.ID)
}

type interruptOnceSearchRebuildFetcher struct {
	syncer.Fetcher

	mu          sync.Mutex
	blockUID    uint32
	uidValidity uint32
	started     chan struct{}
	release     chan struct{}
	blocked     bool
	raw         []byte
}

func (f *interruptOnceSearchRebuildFetcher) FetchMessage(ctx context.Context, _ store.MailAccount, mailbox string, uid uint32) (syncer.FetchedMessage, error) {
	if uid != f.blockUID {
		return syncer.FetchedMessage{}, fmt.Errorf("unexpected rebuild fetch UID %d", uid)
	}
	f.mu.Lock()
	block := !f.blocked
	if block {
		f.blocked = true
		close(f.started)
	}
	f.mu.Unlock()
	if block {
		select {
		case <-f.release:
		case <-ctx.Done():
			return syncer.FetchedMessage{}, ctx.Err()
		}
	}
	return syncer.FetchedMessage{
		Mailbox: mailbox, UID: uid, UIDValidity: f.uidValidity,
		InternalDate: time.Now().UTC(), Size: int64(len(f.raw)), Raw: append([]byte(nil), f.raw...),
	}, nil
}

func (f *interruptOnceSearchRebuildFetcher) MailboxStatus(context.Context, store.MailAccount, string) (syncer.MailboxStatus, error) {
	return syncer.MailboxStatus{}, errors.New("test generation recovery remains paused")
}

func startSearchRebuildRequest(t *testing.T, server *Server, user store.User, mailboxID int64) int64 {
	t.Helper()
	response := httptest.NewRecorder()
	server.handleAPI(response, authenticatedFolderActionRequest(t, server, user,
		fmt.Sprintf("/api/account/folders/%d/search-index/rebuild", mailboxID)))
	if response.Code != http.StatusOK {
		t.Fatalf("start rebuild status=%d body=%s", response.Code, response.Body.String())
	}
	var queued struct {
		RunID int64 `json:"run_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.RunID <= 0 {
		t.Fatalf("start rebuild response=%s", response.Body.String())
	}
	return queued.RunID
}

func createLocalSearchRebuildMessage(t *testing.T, ctx context.Context, db *store.Store, blobStore *blob.Store, user store.User, account store.MailAccount, mailbox store.Mailbox, uid uint32) store.MessageRecord {
	t.Helper()
	raw := []byte(fmt.Sprintf("From: Sender <sender@example.test>\r\nTo: %s\r\nSubject: Local rebuild %d\r\nMessage-ID: <local-rebuild-%d@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nlocal-rebuild-body-%d\r\n", user.Email, uid, uid, uid))
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, uid, raw)
	if err != nil {
		t.Fatal(err)
	}
	blobRecord, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blobRecord.ID,
		MessageIDHeader: fmt.Sprintf("<local-rebuild-%d@example.test>", uid), Subject: fmt.Sprintf("Local rebuild %d", uid),
		FromAddr: "sender@example.test", ToAddr: user.Email, Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: uid, Size: saved.Size, BlobPath: saved.Path, BodyText: fmt.Sprintf("local-rebuild-body-%d", uid),
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}
