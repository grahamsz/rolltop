package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

type searchRepairBatchFetcher struct {
	*moveTestFetcher
	calls       [][]uint32
	singleCalls int
	failAfter   int
	batchErr    error
}

func (f *searchRepairBatchFetcher) FetchMailboxWithUIDValidity(context.Context, store.MailAccount, string, uint32, uint32, func(FetchedMessage) error) error {
	return errors.New("unexpected full mailbox fetch")
}

func (f *searchRepairBatchFetcher) FetchUIDsWithUIDValidity(_ context.Context, _ store.MailAccount, mailbox string, uids []uint32, expectedUIDValidity uint32, handle func(FetchedMessage) error) error {
	f.calls = append(f.calls, append([]uint32(nil), uids...))
	if expectedUIDValidity != moveTestSourceUIDValidity {
		return errors.New("unexpected generation")
	}
	for index, uid := range uids {
		if f.batchErr != nil && index >= f.failAfter {
			return f.batchErr
		}
		raw := []byte(fmt.Sprintf("From: sender@example.test\r\nTo: receiver@example.test\r\nSubject: Batch UID %d\r\nMessage-ID: <batch-%d@example.test>\r\n\r\nbatched full body %d\r\n", uid, uid, uid))
		if err := handle(FetchedMessage{
			Mailbox: mailbox, UID: uid, UIDValidity: expectedUIDValidity,
			InternalDate: time.Now().UTC(), Raw: raw,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *searchRepairBatchFetcher) FetchMessage(context.Context, store.MailAccount, string, uint32) (FetchedMessage, error) {
	f.singleCalls++
	return FetchedMessage{}, errors.New("unexpected single message fetch")
}

func TestRepairMailboxSearchIndexBatchesOnlyRemoteRows(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	searchService, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	blobStore := blob.New(dir)
	remote := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid <= fixture.message.UID+2; uid++ {
		remote = append(remote, createPendingAttachmentIndexMessage(t, ctx, fixture, uid))
	}
	local := createPendingAttachmentIndexMessage(t, ctx, fixture, fixture.message.UID+3)
	localRaw := []byte("From: local@example.test\r\nTo: receiver@example.test\r\nSubject: Local raw\r\n\r\nlocal-only full body needle\r\n")
	saved, err := blobStore.SaveRawMessage(fixture.userID, fixture.account.ID, fixture.source.Name, local.UID, localRaw)
	if err != nil {
		t.Fatal(err)
	}
	local, err = fixture.store.RetainMessageBlob(ctx, fixture.userID, local.ID, store.BlobRecord{
		Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size,
	})
	if err != nil {
		t.Fatal(err)
	}

	fetcher := &searchRepairBatchFetcher{moveTestFetcher: fixture.fetcher, failAfter: -1}
	fixture.service.Search = searchService
	fixture.service.Blobs = blobStore
	fixture.service.Fetcher = fetcher
	indexed, err := fixture.service.RepairMailboxSearchIndex(ctx, fixture.userID, fixture.source, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != len(remote)+1 {
		t.Fatalf("indexed=%d, want %d", indexed, len(remote)+1)
	}
	if len(fetcher.calls) != 1 {
		t.Fatalf("batch calls=%v, want one page call", fetcher.calls)
	}
	if fetcher.singleCalls != 0 {
		t.Fatalf("single fetch calls=%d, want 0", fetcher.singleCalls)
	}
	if len(fetcher.calls[0]) != len(remote) {
		t.Fatalf("batch UIDs=%v, want %d remote rows", fetcher.calls[0], len(remote))
	}
	for _, uid := range fetcher.calls[0] {
		if uid == local.UID {
			t.Fatalf("local UID %d was included in remote batch %v", local.UID, fetcher.calls[0])
		}
	}
	assertSearchContainsMessage(t, ctx, searchService, fixture.userID, "local-only full body needle", local.ID)
	for _, message := range remote {
		assertSearchContainsMessage(t, ctx, searchService, fixture.userID, fmt.Sprintf("batched full body %d", message.UID), message.ID)
	}
}

func TestRepairMailboxSearchIndexBatchFailureTripsRunBreaker(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	searchService, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	messages := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid <= fixture.message.UID+500; uid++ {
		messages = append(messages, createPendingAttachmentIndexMessage(t, ctx, fixture, uid))
	}
	fetcher := &searchRepairBatchFetcher{
		moveTestFetcher: fixture.fetcher,
		failAfter:       1,
		batchErr:        errors.New("sensitive server outage detail"),
	}
	fixture.service.Search = searchService
	fixture.service.Blobs = blob.New(dir)
	fixture.service.Fetcher = fetcher

	var logs bytes.Buffer
	previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	indexed, err := fixture.service.RepairMailboxSearchIndex(ctx, fixture.userID, fixture.source, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != len(messages) {
		t.Fatalf("indexed=%d, want %d", indexed, len(messages))
	}
	if len(fetcher.calls) != 1 || len(fetcher.calls[0]) != 500 {
		t.Fatalf("batch calls=%v, want one 500-row attempt", fetcher.calls)
	}
	if fetcher.singleCalls != 0 {
		t.Fatalf("single fetch calls=%d, want 0", fetcher.singleCalls)
	}
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, messages[0].ID, false)
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, messages[1].ID, true)
	last := messages[len(messages)-1]
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, last.ID, true)
	assertSearchContainsMessage(t, ctx, searchService, fixture.userID, last.Subject, last.ID)

	output := logs.String()
	if strings.Contains(output, "sensitive server outage detail") {
		t.Fatalf("batch log exposed remote error content: %q", output)
	}
	for _, want := range []string{
		"repair mailbox search index remote page deferred",
		"requested=500", "enriched=1", "deferred=499", "breaker=true",
		"indexed=501 deferred=500",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("repair log %q does not contain %q", output, want)
		}
	}
}

func TestRepairMailboxSearchIndexGenerationMismatchNeverHydratesRaw(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	searchService, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	userDB, err := fixture.store.UserDB(ctx, fixture.userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE messages SET uid_validity = ? WHERE user_id = ? AND id = ?`,
		int64(moveTestSourceUIDValidity-1), fixture.userID, fixture.message.ID); err != nil {
		t.Fatal(err)
	}
	originalBlobID := fixture.message.BlobID
	fetcher := &searchRepairBatchFetcher{moveTestFetcher: fixture.fetcher, failAfter: -1}
	fixture.service.Search = searchService
	fixture.service.Blobs = blob.New(dir)
	fixture.service.Fetcher = fetcher
	indexed, err := fixture.service.RepairMailboxSearchIndex(ctx, fixture.userID, fixture.source, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("indexed=%d, want preview fallback", indexed)
	}
	if len(fetcher.calls) != 0 || fetcher.singleCalls != 0 {
		t.Fatalf("generation mismatch performed remote fetches batches=%v singles=%d", fetcher.calls, fetcher.singleCalls)
	}
	message, err := fixture.store.GetMessageForUser(ctx, fixture.userID, fixture.message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.BlobID != originalBlobID || message.BlobPath != "" {
		t.Fatalf("generation mismatch attached raw message=%+v", message)
	}
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, message.ID, true)
	assertSearchContainsMessage(t, ctx, searchService, fixture.userID, message.Subject, message.ID)
}

func assertSearchContainsMessage(t *testing.T, ctx context.Context, service *search.Service, userID int64, query string, messageID int64) {
	t.Helper()
	hits, err := service.Search(ctx, userID, query, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMessageID(hits, messageID) {
		t.Fatalf("search %q hits=%v, want message %d", query, hits, messageID)
	}
}
