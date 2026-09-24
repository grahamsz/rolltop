// File overview: Integration tests for per-folder sync failure isolation.
//
// A failing folder (throttled fetch, failed STATUS) must no longer abort the
// whole account run: the error is recorded with backoff and the remaining
// folders still sync. Metadata reconciliation still runs after a failed fetch
// so server-side moves disappear locally without waiting for a clean turn.

package syncer_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

// flakyMailboxFetcher fails per-folder operations on demand while delegating
// everything else to the shared fakeFetcher.
type flakyMailboxFetcher struct {
	*fakeFetcher
	fetchErrByMailbox  map[string]error
	statusErrByMailbox map[string]error
	uidsOverride       map[string][]uint32
	// deliverBeforeFail hands that many messages to the fetch handler before
	// returning the mailbox's fetch error from the handler, modelling a
	// transport failure mid-batch. The delivered messages must stay durable.
	deliverBeforeFail map[string]int
	// onFetch runs before each fetch call. Tests use it to cancel the
	// context mid-turn.
	onFetch func(mailbox string)

	mu         sync.Mutex
	fetchCalls []string
}

func (f *flakyMailboxFetcher) noteFetch(mailbox string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetchCalls = append(f.fetchCalls, mailbox)
}

func (f *flakyMailboxFetcher) fetchCallsFor(mailbox string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, name := range f.fetchCalls {
		if strings.EqualFold(name, mailbox) {
			count++
		}
	}
	return count
}

func (f *flakyMailboxFetcher) FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(syncer.FetchedMessage) error) error {
	f.noteFetch(mailbox)
	if f.onFetch != nil {
		f.onFetch(mailbox)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.fetchErrByMailbox[strings.ToLower(mailbox)]; err != nil {
		if remaining := f.deliverBeforeFail[strings.ToLower(mailbox)]; remaining > 0 {
			return f.fakeFetcher.FetchMailbox(ctx, account, mailbox, afterUID, func(msg syncer.FetchedMessage) error {
				if remaining <= 0 {
					return err
				}
				remaining--
				return handle(msg)
			})
		}
		return err
	}
	return f.fakeFetcher.FetchMailbox(ctx, account, mailbox, afterUID, handle)
}

func (f *flakyMailboxFetcher) FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, afterUID, expectedUIDValidity uint32, handle func(syncer.FetchedMessage) error) error {
	f.noteFetch(mailbox)
	if f.onFetch != nil {
		f.onFetch(mailbox)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.fetchErrByMailbox[strings.ToLower(mailbox)]; err != nil {
		if remaining := f.deliverBeforeFail[strings.ToLower(mailbox)]; remaining > 0 {
			return f.fakeFetcher.FetchMailboxWithUIDValidity(ctx, account, mailbox, afterUID, expectedUIDValidity, func(msg syncer.FetchedMessage) error {
				if remaining <= 0 {
					return err
				}
				remaining--
				return handle(msg)
			})
		}
		return err
	}
	return f.fakeFetcher.FetchMailboxWithUIDValidity(ctx, account, mailbox, afterUID, expectedUIDValidity, handle)
}

func (f *flakyMailboxFetcher) MailboxStatus(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxStatus, error) {
	if err := f.statusErrByMailbox[strings.ToLower(mailbox)]; err != nil {
		return syncer.MailboxStatus{}, err
	}
	return f.fakeFetcher.MailboxStatus(ctx, account, mailbox)
}

func (f *flakyMailboxFetcher) UIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	if uids, ok := f.uidsOverride[strings.ToLower(mailbox)]; ok {
		return append([]uint32(nil), uids...), nil
	}
	return f.fakeFetcher.UIDs(ctx, account, mailbox)
}

type isolationHarness struct {
	ctx     context.Context
	dir     string
	db      *store.Store
	blobs   *blob.Store
	search  *search.Service
	service *syncer.Service
	fetcher *flakyMailboxFetcher
	user    store.User
	account store.MailAccount
}

func newIsolationHarness(t *testing.T) *isolationHarness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { searchSvc.Close() })
	user, err := db.CreateUser(ctx, "isolation@example.test", "Isolation", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	mailAccount := account(user.ID, encrypted)
	created, err := db.UpsertMailAccount(ctx, mailAccount)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &flakyMailboxFetcher{
		fakeFetcher: &fakeFetcher{
			messages:  map[int64][]syncer.FetchedMessage{},
			mailboxes: []syncer.MailboxInfo{{Name: "INBOX"}, {Name: "Archive/2025"}},
		},
		fetchErrByMailbox:  map[string]error{},
		statusErrByMailbox: map[string]error{},
		uidsOverride:       map[string][]uint32{},
		deliverBeforeFail:  map[string]int{},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}
	h := &isolationHarness{ctx: ctx, dir: dir, db: db, blobs: service.Blobs, search: searchSvc, service: service, fetcher: fetcher, user: user, account: created}
	return h
}

// freshService rebuilds the Service with the same store and fetcher. Backoff
// state lives on the Service, so this clears it while keeping the durable
// checkpoint, modelling a process restart.
func (h *isolationHarness) freshService() {
	h.service = &syncer.Service{Store: h.db, Blobs: h.blobs, Search: h.search, Fetcher: h.fetcher}
}

func (h *isolationHarness) sync(t *testing.T, mailboxes ...string) store.SyncRun {
	t.Helper()
	run, err := h.service.SyncUserMailboxes(h.ctx, h.user.ID, mailboxes)
	if err != nil {
		t.Fatalf("SyncUserMailboxes(%v) returned error: %v", mailboxes, err)
	}
	run, err = h.db.GetSyncRunForUser(h.ctx, h.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func (h *isolationHarness) localMessageCount(t *testing.T, mailbox string) int {
	t.Helper()
	mb, err := h.db.GetMailbox(h.ctx, h.user.ID, h.account.ID, mailbox)
	if err != nil {
		t.Fatal(err)
	}
	count, err := h.db.CountMessagesForMailbox(h.ctx, h.user.ID, mb.ID)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func testFetchedMessage(mailbox string, uid uint32, subject string) syncer.FetchedMessage {
	return syncer.FetchedMessage{
		Mailbox:      mailbox,
		UID:          uid,
		InternalDate: time.Now().UTC(),
		Raw:          []byte(rawMessage("sender@example.test", subject, "body", false)),
	}
}

func TestMailboxFetchFailureDoesNotStarveOtherFolders(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fakeFetcher.messages[h.user.ID] = []syncer.FetchedMessage{
		testFetchedMessage("INBOX", 1, "inbox-one"),
		testFetchedMessage("Archive/2025", 1, "archive-one"),
	}
	h.fetcher.fetchErrByMailbox["inbox"] = errors.New("context deadline exceeded")

	run := h.sync(t, "INBOX", "Archive/2025")
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "INBOX") {
		t.Fatalf("run error text = %q, want it to name the failed folder", run.Error)
	}
	// The healthy folder synced despite the INBOX failure.
	if got := h.localMessageCount(t, "Archive/2025"); got != 1 {
		t.Fatalf("Archive/2025 local messages = %d, want 1", got)
	}
	if got := h.fetcher.fetchCallsFor("Archive/2025"); got != 1 {
		t.Fatalf("Archive/2025 fetch attempts = %d, want 1", got)
	}
	if got := h.fetcher.fetchCallsFor("INBOX"); got != 1 {
		t.Fatalf("INBOX fetch attempts = %d, want 1", got)
	}

	// The next cycle backs the failing folder off while the healthy folder
	// keeps syncing.
	run = h.sync(t, "INBOX", "Archive/2025")
	if got := h.fetcher.fetchCallsFor("INBOX"); got != 1 {
		t.Fatalf("INBOX fetch attempts after backoff = %d, want still 1", got)
	}
	if got := h.fetcher.fetchCallsFor("Archive/2025"); got != 2 {
		t.Fatalf("Archive/2025 fetch attempts = %d, want 2", got)
	}
	if run.Status != "ok" {
		t.Fatalf("backoff-only run status = %q, want ok", run.Status)
	}
}

func TestMailboxFetchFailureStillReconcilesMovedMessages(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fakeFetcher.messages[h.user.ID] = []syncer.FetchedMessage{
		testFetchedMessage("INBOX", 1, "moved-away"),
	}
	run := h.sync(t, "INBOX")
	if run.Status != "ok" {
		t.Fatalf("seed run status = %q, want ok", run.Status)
	}
	if got := h.localMessageCount(t, "INBOX"); got != 1 {
		t.Fatalf("seeded INBOX local messages = %d, want 1", got)
	}

	// The message moved to Archive on the server and the next fetch dies to
	// throttling. Reconciliation must still purge the stale local row even
	// though the fetch failed.
	h.fetcher.fakeFetcher.messages[h.user.ID] = nil
	h.fetcher.fetchErrByMailbox["inbox"] = errors.New("context deadline exceeded")
	run = h.sync(t, "INBOX")
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if got := h.localMessageCount(t, "INBOX"); got != 0 {
		t.Fatalf("INBOX local messages after failed-fetch reconcile = %d, want 0", got)
	}
}

func TestMailboxStatusFailureDegradesPlan(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fakeFetcher.messages[h.user.ID] = []syncer.FetchedMessage{
		testFetchedMessage("INBOX", 1, "inbox-one"),
		testFetchedMessage("Archive/2025", 1, "archive-one"),
	}
	h.fetcher.statusErrByMailbox["inbox"] = errors.New("too many connections")

	run := h.sync(t, "INBOX", "Archive/2025")
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if got := h.localMessageCount(t, "Archive/2025"); got != 1 {
		t.Fatalf("Archive/2025 local messages = %d, want 1", got)
	}
}

func TestGenerationRecoveryBypassesMailboxBackoff(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fetchErrByMailbox["inbox"] = errors.New("context deadline exceeded")

	run := h.sync(t, "INBOX")
	if run.Status != "failed" {
		t.Fatalf("ordinary run status = %q, want failed", run.Status)
	}
	if got := h.fetcher.fetchCallsFor("INBOX"); got != 1 {
		t.Fatalf("INBOX fetch attempts = %d, want 1", got)
	}

	// The recovery worker is the designated retry path: it must still attempt
	// the folder even while ordinary syncs back it off. A failed recovery
	// turn reports the folder failure as an error (the runner keys its retry
	// interval on that), but the attempt itself must happen.
	_, err := h.service.RecoverUserAccountMailboxGeneration(h.ctx, h.user.ID, h.account.ID, "INBOX")
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("RecoverUserAccountMailboxGeneration error = %v, want the folder failure", err)
	}
	if got := h.fetcher.fetchCallsFor("INBOX"); got != 2 {
		t.Fatalf("INBOX fetch attempts after recovery turn = %d, want 2", got)
	}
}

func TestMailboxFetchFailureKeepsPartialCheckpoint(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fakeFetcher.messages[h.user.ID] = []syncer.FetchedMessage{
		testFetchedMessage("INBOX", 1, "one"),
		testFetchedMessage("INBOX", 2, "two"),
		testFetchedMessage("INBOX", 3, "three"),
		testFetchedMessage("INBOX", 4, "four"),
		testFetchedMessage("INBOX", 5, "five"),
	}
	// Deliver three messages, then die mid-batch like a throttled connection.
	h.fetcher.fetchErrByMailbox["inbox"] = errors.New("context deadline exceeded")
	h.fetcher.deliverBeforeFail["inbox"] = 3

	// SyncUser (no explicit mailbox request) takes the ordinary incremental
	// fetch path rather than the explicit-repair path.
	run, err := h.service.SyncUser(h.ctx, h.user.ID)
	if err != nil {
		t.Fatalf("SyncUser returned error: %v", err)
	}
	run, err = h.db.GetSyncRunForUser(h.ctx, h.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	// The three delivered messages and their UID checkpoint are durable.
	if got := h.localMessageCount(t, "INBOX"); got != 3 {
		t.Fatalf("INBOX local messages = %d, want 3", got)
	}
	mb, err := h.db.GetMailbox(h.ctx, h.user.ID, h.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if mb.LastUID != 3 {
		t.Fatalf("INBOX last_uid = %d, want 3", mb.LastUID)
	}

	// The failure clears and the backoff state resets with the process. The
	// resume must continue after the durable checkpoint, not re-download.
	delete(h.fetcher.fetchErrByMailbox, "inbox")
	delete(h.fetcher.deliverBeforeFail, "inbox")
	h.freshService()
	fetchesBeforeResume := len(h.fetcher.fakeFetcher.fetchAfterUIDs)
	run, err = h.service.SyncUser(h.ctx, h.user.ID)
	if err != nil {
		t.Fatalf("resume SyncUser returned error: %v", err)
	}
	run, err = h.db.GetSyncRunForUser(h.ctx, h.user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "ok" {
		t.Fatalf("resume run status = %q, want ok", run.Status)
	}
	if got := h.localMessageCount(t, "INBOX"); got != 5 {
		t.Fatalf("INBOX local messages after resume = %d, want 5", got)
	}
	mb, err = h.db.GetMailbox(h.ctx, h.user.ID, h.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if mb.LastUID != 5 {
		t.Fatalf("INBOX last_uid after resume = %d, want 5", mb.LastUID)
	}
	// The resume fetched after UID 3, so messages 1-3 were not re-downloaded.
	for _, afterUID := range h.fetcher.fakeFetcher.fetchAfterUIDs[fetchesBeforeResume:] {
		if afterUID < 3 {
			t.Fatalf("resume fetched after UID %d, want >= 3", afterUID)
		}
	}
}

func TestMailboxFetchContextCancellationAbortsTurn(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fakeFetcher.messages[h.user.ID] = []syncer.FetchedMessage{
		testFetchedMessage("INBOX", 1, "inbox-one"),
		testFetchedMessage("Archive/2025", 1, "archive-one"),
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	var once sync.Once
	h.fetcher.onFetch = func(mailbox string) {
		if strings.EqualFold(mailbox, "INBOX") {
			once.Do(cancel)
		}
	}

	_, err := h.service.SyncUserMailboxes(ctx, h.user.ID, []string{"INBOX", "Archive/2025"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sync error = %v, want context.Canceled", err)
	}
	// The cancelled turn aborts instead of isolating: the later folder never
	// runs because it shares the expired context.
	if got := h.fetcher.fetchCallsFor("Archive/2025"); got != 0 {
		t.Fatalf("Archive/2025 fetch attempts = %d, want 0", got)
	}
}

func TestMailboxLocalSearchFailurePropagatesWithoutBackoff(t *testing.T) {
	h := newIsolationHarness(t)
	h.fetcher.fakeFetcher.messages[h.user.ID] = []syncer.FetchedMessage{
		testFetchedMessage("INBOX", 1, "inbox-one"),
		testFetchedMessage("INBOX", 2, "inbox-two"),
	}

	// Break the local search index: the fetch itself succeeds, but flushing
	// the search batch is a local failure. It must abort the run with the
	// original error instead of being isolated as a folder failure.
	if err := h.search.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.SyncUser(h.ctx, h.user.ID); err == nil {
		t.Fatal("SyncUser succeeded with a broken search index, want the local error")
	}
	fetchesAfterFailure := h.fetcher.fetchCallsFor("INBOX")

	// Repair the index. The folder must be retried on the very next turn:
	// a local failure records no backoff, so INBOX is fetched again instead
	// of being skipped for 15 minutes.
	searchSvc, err := search.Open(filepath.Join(h.dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { searchSvc.Close() })
	h.search = searchSvc
	h.freshService()

	if _, err := h.service.SyncUser(h.ctx, h.user.ID); err != nil {
		t.Fatalf("retry SyncUser returned error: %v", err)
	}
	if got := h.fetcher.fetchCallsFor("INBOX"); got <= fetchesAfterFailure {
		t.Fatalf("INBOX fetch attempts = %d after failure (was %d): folder was backed off by a local error",
			got, fetchesAfterFailure)
	}
}
