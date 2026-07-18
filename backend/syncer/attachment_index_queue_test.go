package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

type attachmentIndexQueueFetcher struct {
	*moveTestFetcher
	failures map[uint32]error
	calls    map[uint32]int
	mu       sync.Mutex
}

func (f *attachmentIndexQueueFetcher) FetchMessage(_ context.Context, _ store.MailAccount, mailbox string, uid uint32) (FetchedMessage, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = make(map[uint32]int)
	}
	f.calls[uid]++
	err := f.failures[uid]
	f.mu.Unlock()
	if err != nil {
		return FetchedMessage{}, err
	}
	raw := []byte(fmt.Sprintf("From: sender@example.test\r\nTo: receiver@example.test\r\nSubject: Indexed UID %d\r\nMessage-ID: <index-%d@example.test>\r\n\r\nsearch body %d\r\n", uid, uid, uid))
	return FetchedMessage{
		Mailbox:      mailbox,
		UID:          uid,
		UIDValidity:  moveTestSourceUIDValidity,
		InternalDate: time.Now().UTC(),
		Raw:          raw,
	}, nil
}

func (f *attachmentIndexQueueFetcher) callCount(uid uint32) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[uid]
}

func (f *attachmentIndexQueueFetcher) totalCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, count := range f.calls {
		total += count
	}
	return total
}

func TestAttachmentIndexQueueBatchesRemoteRowsByMailbox(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	messages := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid <= fixture.message.UID+24; uid++ {
		messages = append(messages, createPendingAttachmentIndexMessage(t, ctx, fixture, uid))
	}
	fetcher := &searchRepairBatchFetcher{moveTestFetcher: fixture.fetcher, failAfter: -1}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher

	processed, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, len(messages))
	if err != nil {
		t.Fatal(err)
	}
	if processed != len(messages) {
		t.Fatalf("processed=%d, want %d", processed, len(messages))
	}
	if len(fetcher.calls) != 1 || len(fetcher.calls[0]) != len(messages) {
		t.Fatalf("batch calls=%v, want one %d-message fetch", fetcher.calls, len(messages))
	}
	if fetcher.singleCalls != 0 {
		t.Fatalf("single-message fetches=%d, want 0", fetcher.singleCalls)
	}
	for _, message := range messages {
		assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, message.ID, false)
		assertSearchContainsMessage(t, ctx, searchService, fixture.userID,
			fmt.Sprintf("batched full body %d", message.UID), message.ID)
	}
}

func TestAttachmentIndexSkipsUncachedHistoricalMessagesWithoutRemoteHydration(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	fetcher := &attachmentIndexQueueFetcher{moveTestFetcher: fixture.fetcher}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher
	fixture.service.AllowBackgroundAttachmentHydration = false

	processed, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, 25)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 0 {
		t.Fatalf("processed=%d, want 0 after bulk local completion", processed)
	}
	if calls := fetcher.totalCallCount(); calls != 0 {
		t.Fatalf("background attachment hydration calls=%d, want 0", calls)
	}
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, fixture.message.ID, false)
}

func TestAttachmentIndexQueueCheckpointsBeforeForegroundCancellation(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	messages := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid <= fixture.message.UID+9; uid++ {
		messages = append(messages, createPendingAttachmentIndexMessage(t, context.Background(), fixture, uid))
	}
	fetcher := &searchRepairBatchFetcher{
		moveTestFetcher: fixture.fetcher,
		failAfter:       -1,
		cancelAfter:     maintenanceSearchCheckpointSize + 1,
		cancel:          cancel,
	}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher

	processed, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, len(messages))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first maintenance pass error=%v, want context cancellation", err)
	}
	if processed != maintenanceSearchCheckpointSize+1 {
		t.Fatalf("first maintenance pass processed=%d, want %d", processed, maintenanceSearchCheckpointSize+1)
	}
	for index, message := range messages {
		wantPending := index >= maintenanceSearchCheckpointSize
		assertAttachmentIndexPending(t, context.Background(), fixture.store, fixture.userID, message.ID, wantPending)
		if !wantPending {
			assertSearchContainsMessage(t, context.Background(), searchService, fixture.userID,
				fmt.Sprintf("batched full body %d", message.UID), message.ID)
		}
	}

	processed, err = fixture.service.IndexPendingAttachmentsForUser(context.Background(), fixture.userID, len(messages))
	if err != nil {
		t.Fatal(err)
	}
	if processed != len(messages)-maintenanceSearchCheckpointSize {
		t.Fatalf("retry processed=%d, want %d", processed, len(messages)-maintenanceSearchCheckpointSize)
	}
	if len(fetcher.calls) != 2 || len(fetcher.calls[0]) != len(messages) || len(fetcher.calls[1]) != len(messages)-maintenanceSearchCheckpointSize {
		t.Fatalf("remote pages=%v, want initial %d rows then %d pending rows", fetcher.calls, len(messages), len(messages)-maintenanceSearchCheckpointSize)
	}
	for _, message := range messages {
		assertAttachmentIndexPending(t, context.Background(), fixture.store, fixture.userID, message.ID, false)
	}
}

func TestAttachmentIndexFailureDoesNotPinHigherMessageIDs(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	fetcher := &attachmentIndexQueueFetcher{
		moveTestFetcher: fixture.fetcher,
		failures: map[uint32]error{
			fixture.message.UID: errors.New("sensitive remote response and raw body"),
		},
	}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher
	messages := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid <= fixture.message.UID+3; uid++ {
		messages = append(messages, createPendingAttachmentIndexMessage(t, ctx, fixture, uid))
	}

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

	if n, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, 2); err != nil || n != 2 {
		t.Fatalf("first attachment index batch processed=%d err=%v, want 2, nil", n, err)
	}
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, messages[0].ID, true)
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, messages[1].ID, false)
	hits, err := searchService.Search(ctx, fixture.userID, "manual", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMessageID(hits, messages[0].ID) {
		t.Fatalf("fallback search hits = %v, want failed message %d", hits, messages[0].ID)
	}

	if n, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, 2); err != nil || n != 2 {
		t.Fatalf("second attachment index batch processed=%d err=%v, want 2, nil", n, err)
	}
	for _, message := range messages[1:] {
		assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, message.ID, false)
	}

	// The cursor wraps only after higher IDs have progressed. The failed row is
	// then skipped during its cooldown rather than fetching it in a tight loop.
	if n, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, 2); err != nil || n != 1 {
		t.Fatalf("wrapped attachment index batch processed=%d err=%v, want 1, nil", n, err)
	}
	if fetcher.callCount(fixture.message.UID) != 1 {
		t.Fatalf("failed UID fetch calls = %d, want 1 during cooldown", fetcher.callCount(fixture.message.UID))
	}
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, messages[0].ID, true)

	output := logs.String()
	if strings.Contains(output, "sensitive remote response") || strings.Contains(output, "raw body") {
		t.Fatalf("attachment index log exposed error content: %q", output)
	}
	for _, want := range []string{
		fmt.Sprintf("user_id=%d", fixture.userID),
		fmt.Sprintf("account_id=%d", fixture.account.ID),
		fmt.Sprintf("mailbox_id=%d", fixture.source.ID),
		fmt.Sprintf("message_id=%d", fixture.message.ID),
		fmt.Sprintf("uid=%d", fixture.message.UID),
		"stage=raw-fetch",
		"error_type=*errors.errorString",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("attachment index log %q does not contain %q", output, want)
		}
	}
}

func TestRepairMailboxSearchIndexFailureDoesNotPinHigherMessageIDs(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	second := createPendingAttachmentIndexMessage(t, ctx, fixture, fixture.message.UID+1)
	fetcher := &attachmentIndexQueueFetcher{
		moveTestFetcher: fixture.fetcher,
		failures: map[uint32]error{
			fixture.message.UID: errors.New("sensitive remote response and raw body"),
		},
	}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher
	if err := fixture.store.MarkMessageAttachmentIndexed(ctx, fixture.userID, fixture.message.ID, fixture.message.HasAttachments); err != nil {
		t.Fatal(err)
	}

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
	if indexed != 2 {
		t.Fatalf("repair indexed=%d, want 2", indexed)
	}
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, fixture.message.ID, true)
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, second.ID, false)
	pending, err := fixture.store.ListMessagesNeedingAttachmentIndex(ctx, fixture.userID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != fixture.message.ID {
		t.Fatalf("messages eligible for later enrichment=%v, want failed message %d", pending, fixture.message.ID)
	}

	fallbackHits, err := searchService.Search(ctx, fixture.userID, "manual", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMessageID(fallbackHits, fixture.message.ID) {
		t.Fatalf("fallback search hits=%v, want failed message %d", fallbackHits, fixture.message.ID)
	}
	laterHits, err := searchService.Search(ctx, fixture.userID, fmt.Sprintf("body %d", second.UID), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsMessageID(laterHits, second.ID) {
		t.Fatalf("later message search hits=%v, want %d", laterHits, second.ID)
	}
	if fetcher.callCount(fixture.message.UID) != 1 || fetcher.callCount(second.UID) != 1 {
		t.Fatalf("repair fetch calls failed=%d later=%d, want one each", fetcher.callCount(fixture.message.UID), fetcher.callCount(second.UID))
	}

	output := logs.String()
	if strings.Contains(output, "sensitive remote response") || strings.Contains(output, "raw body") {
		t.Fatalf("repair log exposed error content: %q", output)
	}
	for _, want := range []string{
		"repair mailbox search index legacy remote page deferred",
		"requested=2",
		"enriched=1",
		"deferred=1",
		"error_type=*errors.errorString",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("repair log %q does not contain %q", output, want)
		}
	}
}

func containsMessageID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestAttachmentIndexAllFailureBatchStopsAfterOneCursorCycle(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	second := createPendingAttachmentIndexMessage(t, ctx, fixture, fixture.message.UID+1)
	fetcher := &attachmentIndexQueueFetcher{
		moveTestFetcher: fixture.fetcher,
		failures: map[uint32]error{
			fixture.message.UID: errors.New("first unavailable"),
			second.UID:          errors.New("second unavailable"),
		},
	}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher

	fixture.service.attachmentIndexContinuationDelay = 20 * time.Millisecond
	if n, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, 2); err != nil || n != 0 {
		t.Fatalf("initial failed batch processed=%d err=%v, want 0, nil", n, err)
	}
	if n, err := fixture.service.IndexPendingAttachmentsForUser(ctx, fixture.userID, 2); err != nil || n != 0 {
		t.Fatalf("wrapped cooling-down batch processed=%d err=%v, want 0, nil", n, err)
	}
	if fetcher.callCount(fixture.message.UID) != 1 || fetcher.callCount(second.UID) != 1 {
		t.Fatalf("cooldown fetch calls first=%d second=%d, want one per UID",
			fetcher.callCount(fixture.message.UID), fetcher.callCount(second.UID))
	}
}

func TestRunnerYieldsBetweenAllDeferredAttachmentPages(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	messages := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid < fixture.message.UID+75; uid++ {
		messages = append(messages, createPendingAttachmentIndexMessage(t, ctx, fixture, uid))
	}
	failures := make(map[uint32]error, len(messages))
	for _, message := range messages {
		failures[message.UID] = errors.New("global IMAP outage")
	}
	fetcher := &attachmentIndexQueueFetcher{
		moveTestFetcher: fixture.fetcher,
		failures:        failures,
	}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher
	fixture.service.attachmentIndexContinuationDelay = 80 * time.Millisecond
	// A late wake can begin beyond the current maximum ID and wrap immediately.
	// Fresh failures on that wrapped page must still yield forward to later IDs.
	fixture.service.advanceAttachmentIndexCursor(fixture.userID, messages[len(messages)-1].ID+1)
	runner := NewRunnerWithContext(ctx, fixture.service)

	if !runner.StartAttachmentIndex(fixture.userID) {
		t.Fatal("initial attachment index did not start")
	}
	for _, want := range []int{25, 50, 75} {
		waitForAttachmentFetchCalls(t, fetcher, want)
		waitForRunnerMaintenanceIdle(t, runner, fixture.userID)
		waitForAttachmentRetryTimer(t, runner, fixture.userID, true)
		if got := fetcher.totalCallCount(); got != want {
			t.Fatalf("attachment fetch calls = %d after page, want %d", got, want)
		}
		if want == 25 {
			if runner.StartAttachmentIndex(fixture.userID) {
				t.Fatal("unrelated attachment start bypassed continuation delay")
			}
		}
		time.Sleep(25 * time.Millisecond)
		if got := fetcher.totalCallCount(); got != want {
			t.Fatalf("attachment queue swept without yielding: calls=%d want=%d", got, want)
		}
	}
}

func TestRunnerDelayedContinuationReachesHealthyNextPage(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	searchService, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = searchService.Close() })
	messages := []store.MessageRecord{fixture.message}
	for uid := fixture.message.UID + 1; uid < fixture.message.UID+50; uid++ {
		messages = append(messages, createPendingAttachmentIndexMessage(t, ctx, fixture, uid))
	}
	failures := make(map[uint32]error, attachmentIndexBatchSize)
	for _, message := range messages[:attachmentIndexBatchSize] {
		failures[message.UID] = errors.New("first IMAP page unavailable")
	}
	fetcher := &attachmentIndexQueueFetcher{
		moveTestFetcher: fixture.fetcher,
		failures:        failures,
	}
	fixture.service.Search = searchService
	fixture.service.Fetcher = fetcher
	fixture.service.attachmentIndexContinuationDelay = 80 * time.Millisecond
	runner := NewRunnerWithContext(ctx, fixture.service)

	if !runner.StartAttachmentIndex(fixture.userID) {
		t.Fatal("initial attachment index did not start")
	}
	waitForAttachmentFetchCalls(t, fetcher, attachmentIndexBatchSize)
	waitForRunnerMaintenanceIdle(t, runner, fixture.userID)
	waitForAttachmentRetryTimer(t, runner, fixture.userID, true)
	healthy := messages[attachmentIndexBatchSize]
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, healthy.ID, true)
	if runner.StartAttachmentIndex(fixture.userID) {
		t.Fatal("unrelated attachment start bypassed continuation delay")
	}
	time.Sleep(25 * time.Millisecond)
	assertAttachmentIndexPending(t, ctx, fixture.store, fixture.userID, healthy.ID, true)
	if got := fetcher.totalCallCount(); got != attachmentIndexBatchSize {
		t.Fatalf("healthy page fetched before continuation: calls=%d", got)
	}

	waitForAttachmentIndexed(t, fixture.store, fixture.userID, healthy.ID)
	if got := fetcher.totalCallCount(); got <= attachmentIndexBatchSize {
		t.Fatalf("delayed continuation fetch calls = %d, want progress beyond the failed page", got)
	}
}

func TestRunnerAutomaticallyWakesAtEarliestAttachmentRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "attachment-retry@example.test", "Attachment Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db}
	early := time.Now().Add(150 * time.Millisecond)
	late := time.Now().Add(600 * time.Millisecond)
	setAttachmentRetryForTest(service, user.ID, 101, late)
	setAttachmentRetryForTest(service, user.ID, 102, early)

	runner := NewRunnerWithContext(ctx, service)
	var callCount atomic.Int32
	calls := make(chan time.Time, 4)
	runner.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
		call := callCount.Add(1)
		calls <- time.Now()
		if call == 2 {
			service.clearAttachmentIndexRetry(user.ID, 101)
			service.clearAttachmentIndexRetry(user.ID, 102)
		}
		return 0, nil
	}
	if !runner.StartAttachmentIndex(user.ID) {
		t.Fatal("initial attachment index did not start")
	}
	first := awaitAttachmentRetryCall(t, calls, "initial attachment index")
	waitForAttachmentRetryTimer(t, runner, user.ID, true)

	// Repeated scheduling replaces the existing per-user timer.
	runner.scheduleNextAttachmentIndexRetry(user.ID)
	runner.scheduleNextAttachmentIndexRetry(user.ID)
	runner.mu.Lock()
	timerCount := len(runner.attachmentRetryTimers)
	runner.mu.Unlock()
	if timerCount != 1 {
		t.Fatalf("attachment retry timers = %d, want 1", timerCount)
	}
	select {
	case <-calls:
		t.Fatal("attachment retry fired before the earliest cooldown")
	case <-time.After(60 * time.Millisecond):
	}
	second := awaitAttachmentRetryCall(t, calls, "automatic attachment retry")
	if elapsed := second.Sub(first); elapsed < 100*time.Millisecond || elapsed >= 500*time.Millisecond {
		t.Fatalf("automatic retry elapsed = %s, want earliest cooldown near 150ms", elapsed)
	}
	waitForRunnerMaintenanceIdle(t, runner, user.ID)
	waitForAttachmentRetryTimer(t, runner, user.ID, false)
	select {
	case <-calls:
		t.Fatal("duplicate or later attachment retry timer fired")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRunnerRetryWakeDropsDueKeyForDeletedMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "attachment-retry-deleted@example.test", "Attachment Retry Deleted", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db}
	// The retry key intentionally has no corresponding SQLite message row.
	setAttachmentRetryForTest(service, user.ID, 9999, time.Now().Add(100*time.Millisecond))
	runner := NewRunnerWithContext(ctx, service)
	calls := make(chan time.Time, 3)
	runner.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
		calls <- time.Now()
		return 0, nil
	}
	if !runner.StartAttachmentIndex(user.ID) {
		t.Fatal("initial attachment index did not start")
	}
	awaitAttachmentRetryCall(t, calls, "initial attachment index")
	waitForAttachmentRetryTimer(t, runner, user.ID, true)
	awaitAttachmentRetryCall(t, calls, "deleted-message retry wake")
	waitForRunnerMaintenanceIdle(t, runner, user.ID)
	waitForAttachmentRetryTimer(t, runner, user.ID, false)
	if retryAt, ok := service.nextAttachmentIndexRetry(user.ID); ok {
		t.Fatalf("deleted-message retry remains scheduled at %s", retryAt)
	}
	select {
	case <-calls:
		t.Fatal("deleted-message retry entered a zero-delay loop")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRunnerCancelsAttachmentRetryTimerOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "attachment-retry-shutdown@example.test", "Attachment Retry Shutdown", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: db}
	setAttachmentRetryForTest(service, user.ID, 201, time.Now().Add(120*time.Millisecond))
	runner := NewRunnerWithContext(ctx, service)
	calls := make(chan time.Time, 2)
	runner.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
		calls <- time.Now()
		return 0, nil
	}
	if !runner.StartAttachmentIndex(user.ID) {
		t.Fatal("initial attachment index did not start")
	}
	awaitAttachmentRetryCall(t, calls, "initial attachment index")
	waitForAttachmentRetryTimer(t, runner, user.ID, true)
	cancel()
	waitForAttachmentRetryTimer(t, runner, user.ID, false)
	select {
	case <-calls:
		t.Fatal("attachment retry ran after Runner shutdown")
	case <-time.After(180 * time.Millisecond):
	}
}

func setAttachmentRetryForTest(service *Service, userID, messageID int64, retryAt time.Time) {
	service.attachmentIndexMu.Lock()
	defer service.attachmentIndexMu.Unlock()
	if service.attachmentIndexRetryAfter == nil {
		service.attachmentIndexRetryAfter = make(map[attachmentIndexRetryKey]time.Time)
	}
	service.attachmentIndexRetryAfter[attachmentIndexRetryKey{userID: userID, messageID: messageID}] = retryAt
}

func awaitAttachmentRetryCall(t *testing.T, calls <-chan time.Time, name string) time.Time {
	t.Helper()
	select {
	case calledAt := <-calls:
		return calledAt
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return time.Time{}
	}
}

func waitForAttachmentRetryTimer(t *testing.T, runner *Runner, userID int64, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		runner.mu.Lock()
		present := runner.attachmentRetryTimers[userID] != nil
		runner.mu.Unlock()
		if present == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("attachment retry timer present = %t, want %t", present, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForAttachmentFetchCalls(t *testing.T, fetcher *attachmentIndexQueueFetcher, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := fetcher.totalCallCount(); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("attachment fetch calls = %d, want at least %d", fetcher.totalCallCount(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForAttachmentIndexed(t *testing.T, db *store.Store, userID, messageID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		message, err := db.GetMessageForUser(context.Background(), userID, messageID)
		if err != nil {
			t.Fatal(err)
		}
		if !message.AttachmentIndexedAt.IsZero() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %d was not indexed after delayed continuation", messageID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func createPendingAttachmentIndexMessage(t *testing.T, ctx context.Context, fixture moveTestFixture, uid uint32) store.MessageRecord {
	t.Helper()
	blobRecord, err := fixture.store.CreateBlob(ctx, store.BlobRecord{
		UserID: fixture.userID, Kind: "message-remote", Path: fmt.Sprintf("remote/index-%d.eml", uid),
		SHA256: fmt.Sprintf("index-%d", uid), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := fixture.store.CreateMessage(ctx, store.CreateMessage{
		UserID: fixture.userID, AccountID: fixture.account.ID, MailboxID: fixture.source.ID, BlobID: blobRecord.ID,
		MessageIDHeader: fmt.Sprintf("<pending-index-%d@example.test>", uid), Subject: fmt.Sprintf("Pending index %d", uid),
		FromAddr: "sender@example.test", ToAddr: "receiver@example.test",
		Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: uid,
		UIDValidity: int64(moveTestSourceUIDValidity), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func assertAttachmentIndexPending(t *testing.T, ctx context.Context, db *store.Store, userID, messageID int64, want bool) {
	t.Helper()
	message, err := db.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if got := message.AttachmentIndexedAt.IsZero(); got != want {
		t.Fatalf("message %d attachment index pending = %t, want %t", messageID, got, want)
	}
}
