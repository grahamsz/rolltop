package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

type ordinaryRecoveryBypassFetcher struct {
	*moveTestFetcher
	deferWork *atomic.Bool
	uidsCalls atomic.Int32
}

type importRetryBatchFetcher struct {
	*ordinaryRecoveryBypassFetcher
	count uint32
}

func (f *importRetryBatchFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	f.deferWork.Store(true)
	return MailboxStatus{Messages: f.count, UIDNext: f.count + 1, UIDValidity: 7}, nil
}

func (f *importRetryBatchFetcher) FetchMailbox(_ context.Context, _ store.MailAccount, mailbox string,
	afterUID uint32, handle func(FetchedMessage) error,
) error {
	for uid := afterUID + 1; uid <= f.count; uid++ {
		if err := handle(FetchedMessage{
			Mailbox: mailbox, UID: uid, UIDValidity: 7, InternalDate: time.Now().UTC(),
			Raw: []byte(fmt.Sprintf("Message-ID: <retry-import-%d@example.test>\r\n"+
				"From: sender@example.test\r\nTo: owner@example.test\r\n"+
				"Subject: Retry import %d\r\n\r\nretryimportsearchtoken\r\n", uid, uid)),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *importRetryBatchFetcher) FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount,
	mailbox string, afterUID, expectedUIDValidity uint32, handle func(FetchedMessage) error,
) error {
	if expectedUIDValidity != 7 {
		return store.ErrMailboxGenerationChanged
	}
	return f.FetchMailbox(ctx, account, mailbox, afterUID, handle)
}

func (f *ordinaryRecoveryBypassFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	f.deferWork.Store(true)
	return MailboxStatus{Messages: 1, UIDNext: 2, UIDValidity: 7}, nil
}

func (f *ordinaryRecoveryBypassFetcher) UIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	f.uidsCalls.Add(1)
	return []uint32{1}, nil
}

func (f *ordinaryRecoveryBypassFetcher) FetchMailbox(_ context.Context, _ store.MailAccount, mailbox string,
	_ uint32, handle func(FetchedMessage) error,
) error {
	return handle(FetchedMessage{
		Mailbox: mailbox, UID: 1, UIDValidity: 7, InternalDate: time.Now().UTC(),
		Raw: []byte("Message-ID: <recovery-bypass@example.test>\r\n" +
			"From: sender@example.test\r\nTo: owner@example.test\r\n" +
			"Subject: Recovery bypass\r\n\r\nrecoverybypasssearchtoken\r\n"),
	})
}

func (f *ordinaryRecoveryBypassFetcher) FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount,
	mailbox string, afterUID, expectedUIDValidity uint32, handle func(FetchedMessage) error,
) error {
	if expectedUIDValidity != 7 {
		return store.ErrMailboxGenerationChanged
	}
	return f.FetchMailbox(ctx, account, mailbox, afterUID, handle)
}

func (f *ordinaryRecoveryBypassFetcher) FetchUIDsWithUIDValidity(context.Context, store.MailAccount,
	string, []uint32, uint32, func(FetchedMessage) error,
) error {
	f.uidsCalls.Add(1)
	return nil
}

func TestStoreFetchedMessageDefersAttachmentSearchExtraction(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	fixture.service.Blobs = blob.New(t.TempDir())
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	fixture.service.Search = searchService
	if !fixture.source.IncludeInSearch {
		t.Fatal("test mailbox must be visible in search")
	}

	marker := filepath.Join(t.TempDir(), "pdftotext-called")
	binDir := t.TempDir()
	pdftotext := filepath.Join(binDir, "pdftotext")
	if err := os.WriteFile(pdftotext, []byte("#!/bin/sh\n: > \"$ROLLTOP_TEST_PDF_MARKER\"\nprintf deferredattachmenttoken\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLTOP_TEST_PDF_MARKER", marker)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	raw := []byte("From: sender@example.test\r\n" +
		"To: receiver@example.test\r\n" +
		"Subject: Deferred search preparation\r\n" +
		"Date: Tue, 14 Jul 2026 12:00:00 +0000\r\n" +
		"Message-ID: <deferred-search@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=rolltop-deferred\r\n\r\n" +
		"--rolltop-deferred\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n" +
		"--rolltop-deferred\r\nContent-Type: application/pdf; name=note.pdf\r\n" +
		"Content-Disposition: attachment; filename=note.pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\nJVBERi10ZXN0\r\n" +
		"--rolltop-deferred--\r\n")
	item := FetchedMessage{
		Mailbox:      fixture.source.Name,
		UID:          74,
		UIDValidity:  uint32(fixture.source.UIDValidity),
		InternalDate: time.Date(2026, 7, 14, 12, 1, 0, 0, time.UTC),
		Raw:          raw,
	}

	msg, _, pendingIndex, err := fixture.service.storeFetchedMessage(ctx, fixture.userID,
		fixture.account, fixture.source, item, false)
	if err != nil {
		t.Fatal(err)
	}
	if pendingIndex != nil {
		t.Fatal("deferred storage prepared a search document")
	}
	if !msg.HasAttachments || !msg.AttachmentIndexedAt.IsZero() {
		t.Fatalf("deferred message attachments=%t indexed_at=%s", msg.HasAttachments, msg.AttachmentIndexedAt)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("attachment text extractor ran during deferred storage: %v", err)
	}
	attachments, err := fixture.store.ListAttachmentsForMessage(ctx, fixture.userID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "note.pdf" || attachments[0].IsInline {
		t.Fatalf("deferred attachment metadata = %+v", attachments)
	}
	hits, err := searchService.Search(ctx, fixture.userID, "deferredattachmenttoken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("deferred search hits = %v, want none", hits)
	}

	if err := fixture.service.IndexAttachmentsForMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("deferred attachment extractor did not run: %v", err)
	}
	hits, err = searchService.Search(ctx, fixture.userID, "deferredattachmenttoken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != msg.ID {
		t.Fatalf("post-index search hits = %v, want message %d", hits, msg.ID)
	}
	indexed, err := fixture.store.GetMessageForUser(ctx, fixture.userID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if indexed.AttachmentIndexedAt.IsZero() {
		t.Fatal("post-index message was not marked attachment-indexed")
	}
}

func TestOrdinaryInboxDefersBulkMaintenanceWhenRecoveryAppearsMidSync(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox := createRunnerMailboxFixture(t, ctx, db, "recovery-bypass@example.test")
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 0, 0, 1, 7); err != nil {
		t.Fatal(err)
	}

	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()
	var deferWork atomic.Bool
	fetcher := &ordinaryRecoveryBypassFetcher{moveTestFetcher: &moveTestFetcher{}, deferWork: &deferWork}
	service := &Service{Store: db, Blobs: blob.New(t.TempDir()), Search: searchService, Fetcher: fetcher}
	if _, err := service.syncUserAccountMailboxes(ctx, user.ID, account.ID, []string{mailbox.Name}, syncAccountOptions{
		deferMaintenance: deferWork.Load,
	}); err != nil {
		t.Fatal(err)
	}
	if fetcher.uidsCalls.Load() != 0 {
		t.Fatalf("sparse repair ran during recovery: UIDs calls=%d", fetcher.uidsCalls.Load())
	}

	msg, err := db.GetMessageByUID(ctx, user.ID, account.ID, mailbox.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if msg.AttachmentIndexedAt.IsZero() {
		t.Fatal("recovery bypass did not preserve per-message search ordering")
	}
	lastUIDs, err := db.LastUIDs(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lastUIDs[mailbox.Name] != 1 {
		t.Fatalf("Inbox checkpoint=%d, want 1", lastUIDs[mailbox.Name])
	}
	hits, err := searchService.Search(ctx, user.ID, "recoverybypasssearchtoken", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != msg.ID {
		t.Fatalf("recovery message search hits=%v, want message %d", hits, msg.ID)
	}
}

func TestIncrementalSyncRetriesMessageWhoseDerivedImportDidNotComplete(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox := createRunnerMailboxFixture(t, ctx, db, "retry-import@example.test")
	const messageCount = fetchedSearchIndexBatchSize
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 0, 0, messageCount, 7); err != nil {
		t.Fatal(err)
	}

	failedSearch, err := search.Open(filepath.Join(t.TempDir(), "failed-bleve"))
	if err != nil {
		t.Fatal(err)
	}
	if err := failedSearch.Close(); err != nil {
		t.Fatal(err)
	}
	var deferWork atomic.Bool
	fetcher := &importRetryBatchFetcher{
		ordinaryRecoveryBypassFetcher: &ordinaryRecoveryBypassFetcher{
			moveTestFetcher: &moveTestFetcher{}, deferWork: &deferWork,
		},
		count: messageCount,
	}
	service := &Service{Store: db, Blobs: blob.New(t.TempDir()), Search: failedSearch, Fetcher: fetcher}
	options := syncAccountOptions{deferMaintenance: deferWork.Load}
	if _, err := service.syncUserAccountMailboxes(ctx, user.ID, account.ID, []string{mailbox.Name}, options); err == nil {
		t.Fatal("sync with closed search service unexpectedly succeeded")
	}

	msg, err := db.GetMessageByUID(ctx, user.ID, account.ID, mailbox.ID, messageCount)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, messageCount, 7)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("partially imported message was treated as complete")
	}
	lastUIDs, err := db.LastUIDs(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lastUIDs[mailbox.Name] != 0 {
		t.Fatalf("failed import advanced checkpoint to %d", lastUIDs[mailbox.Name])
	}
	completedAhead, err := db.GetMessageByUID(ctx, user.ID, account.ID, mailbox.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageImportCompleted(ctx, user.ID, completedAhead.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.syncUserAccountMailboxes(ctx, user.ID, account.ID, []string{mailbox.Name}, options); err == nil {
		t.Fatal("second sync with closed search service unexpectedly succeeded")
	}
	lastUIDs, err = db.LastUIDs(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lastUIDs[mailbox.Name] != 0 {
		t.Fatalf("completed higher UID advanced past a pending lower import: %d", lastUIDs[mailbox.Name])
	}

	healthySearch, err := search.Open(filepath.Join(t.TempDir(), "healthy-bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer healthySearch.Close()
	if err := healthySearch.IndexMessage(ctx, completedAhead, nil); err != nil {
		t.Fatal(err)
	}
	service.Search = healthySearch
	if _, err := service.syncUserAccountMailboxes(ctx, user.ID, account.ID, []string{mailbox.Name}, options); err != nil {
		t.Fatal(err)
	}
	exists, err = db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, messageCount, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("retried message import was not marked complete")
	}
	lastUIDs, err = db.LastUIDs(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lastUIDs[mailbox.Name] != messageCount {
		t.Fatalf("completed retry checkpoint=%d, want %d", lastUIDs[mailbox.Name], messageCount)
	}
	count, err := db.CountMessagesForMailbox(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != messageCount {
		t.Fatalf("retried message count=%d, want %d", count, messageCount)
	}
	hits, err := healthySearch.Search(ctx, user.ID, "retryimportsearchtoken", messageCount+1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != messageCount || !slices.Contains(hits, msg.ID) {
		t.Fatalf("retried search hits=%v, want %d messages including %d", hits, messageCount, msg.ID)
	}
}
