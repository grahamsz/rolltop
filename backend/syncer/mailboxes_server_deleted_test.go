// File overview: Tests for server-side folder deletion reconciliation.
//
// When an account uses "*" folder discovery, a completed IMAP LIST is
// a nonempty listing queues tracked cleanup. The worker rechecks the server
// and settings before pruning; standalone callers run the same tracked task
// synchronously. Protected folders and failed/empty listings never purge.

package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type serverDeletedFixture struct {
	ctx     context.Context
	db      *store.Store
	user    store.User
	account store.MailAccount
	fetcher *fakeFetcher
	service *syncer.Service
}

func newServerDeletedFixture(t *testing.T, mailboxPattern string) *serverDeletedFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user, err := db.CreateUser(ctx, "server-deleted@example.test", "Server Deleted", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	mailAccount := account(user.ID, encrypted)
	mailAccount.Mailbox = mailboxPattern
	created, err := db.UpsertMailAccount(ctx, mailAccount)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{
		mailboxes: []syncer.MailboxInfo{{Name: "INBOX"}},
		messages:  map[int64][]syncer.FetchedMessage{user.ID: {}},
	}
	return &serverDeletedFixture{
		ctx:     ctx,
		db:      db,
		user:    user,
		account: created,
		fetcher: fetcher,
		service: &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher},
	}
}

func (f *serverDeletedFixture) seedFolder(t *testing.T, name, role string) store.Mailbox {
	t.Helper()
	mb, err := f.db.GetOrCreateMailboxWithRole(f.ctx, f.user.ID, f.account.ID, name, role)
	if err != nil {
		t.Fatal(err)
	}
	return mb
}

func (f *serverDeletedFixture) discover(t *testing.T) {
	t.Helper()
	if _, err := f.service.DiscoverMailboxes(f.ctx, f.user.ID); err != nil {
		t.Fatal(err)
	}
}

func (f *serverDeletedFixture) assertFolderGone(t *testing.T, name string) {
	t.Helper()
	if _, err := f.db.GetMailbox(f.ctx, f.user.ID, f.account.ID, name); !store.IsNotFound(err) {
		t.Fatalf("GetMailbox(%q) after prune: err = %v, want not found", name, err)
	}
}

func (f *serverDeletedFixture) assertFolderPresent(t *testing.T, name string) {
	t.Helper()
	if _, err := f.db.GetMailbox(f.ctx, f.user.ID, f.account.ID, name); err != nil {
		t.Fatalf("GetMailbox(%q): err = %v, want folder present", name, err)
	}
}

func TestServerDeletedFolderPrunedWithTrackedRun(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "OldFolder", "")

	f.discover(t)

	f.assertFolderGone(t, "OldFolder")
	runs, err := f.db.ListSyncRunsForUser(f.ctx, f.user.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "ok" || runs[0].MailboxesDone != 1 {
		t.Fatalf("cleanup run = %+v, err=%v", runs, err)
	}
}

func (f *serverDeletedFixture) seedMessage(t *testing.T, mb store.Mailbox, uid uint32) store.MessageRecord {
	t.Helper()
	raw := []byte(rawMessage("sender@example.test", fmt.Sprintf("cleanup %d", uid), "body", false))
	saved, err := f.service.Blobs.SaveRawMessage(mb.UserID, mb.AccountID, mb.Name, uid, raw)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := f.db.CreateBlob(f.ctx, store.BlobRecord{UserID: mb.UserID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := f.db.CreateMessage(f.ctx, store.CreateMessage{UserID: mb.UserID, AccountID: mb.AccountID, MailboxID: mb.ID, BlobID: bl.ID, UID: uid, BlobPath: saved.Path, Subject: fmt.Sprintf("cleanup %d", uid)})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestServerDeletedEmptyListingPreservesLocalMirror(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "OldFolder", "")
	f.service.Fetcher = &errListFetcher{fakeFetcher: f.fetcher}
	f.discover(t)
	f.assertFolderPresent(t, "OldFolder")
}

func TestServerDeletedSchedulingFailurePropagates(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "OldFolder", "")
	want := errors.New("cannot create cleanup run")
	f.service.QueueServerDeletedMailbox = func(int64, store.Mailbox) error { return want }
	if _, err := f.service.DiscoverMailboxes(f.ctx, f.user.ID); !errors.Is(err, want) {
		t.Fatalf("discovery error = %v, want scheduler error", err)
	}
	f.assertFolderPresent(t, "OldFolder")
}

func TestServerDeletedCleanupPreservesOtherTenantMailBlobsAndSearch(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	first := f.seedFolder(t, "OldFolder", "")
	other, err := f.db.CreateUser(f.ctx, "other-cleanup@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := f.db.UpsertMailAccount(f.ctx, account(other.ID, "encrypted"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.db.GetOrCreateMailbox(f.ctx, other.ID, otherAccount.ID, "OldFolder")
	if err != nil {
		t.Fatal(err)
	}
	m1, m2 := f.seedMessage(t, first, 1), f.seedMessage(t, second, 1)
	index, err := search.OpenPerUser(filepath.Join(t.TempDir(), "users"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { index.Close() })
	f.service.Search = index
	if err := index.IndexMessages(f.ctx, []search.MessageIndexDocument{{Message: m1}, {Message: m2}}); err != nil {
		t.Fatal(err)
	}
	f.discover(t)
	f.assertFolderGone(t, "OldFolder")
	if _, err := f.db.GetMessageForUser(f.ctx, other.ID, m2.ID); err != nil {
		t.Fatal(err)
	}
	file, err := f.service.Blobs.OpenUserBlob(other.ID, m2.BlobPath)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if file, err := f.service.Blobs.OpenUserBlob(f.user.ID, m1.BlobPath); !os.IsNotExist(err) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("purged blob still present: %v", err)
	}
	if count, err := index.CountMailboxMessages(f.ctx, other.ID, second.ID); err != nil || count != 1 {
		t.Fatalf("other tenant search count=%d, err=%v", count, err)
	}
	if count, err := index.CountMailboxMessages(f.ctx, f.user.ID, first.ID); err != nil || count != 0 {
		t.Fatalf("purged search count=%d, err=%v", count, err)
	}
	runs, err := f.db.ListSyncRunsForUser(f.ctx, other.ID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("other tenant cleanup runs=%+v, err=%v", runs, err)
	}
}

func TestServerDeletedCleanupRecordsPartialProgressOnCancellation(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	mb := f.seedFolder(t, "OldFolder", "")
	for uid := uint32(1); uid <= 25; uid++ {
		f.seedMessage(t, mb, uid)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.service.NotifyProgress = func(userID int64) {
		runs, err := f.db.ListSyncRunsForUser(f.ctx, userID, 10)
		if err == nil && len(runs) > 0 && runs[0].MessagesSeen >= 10 {
			cancel()
		}
	}
	if _, err := f.service.DiscoverMailboxes(ctx, f.user.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup error=%v, want cancellation", err)
	}
	runs, err := f.db.ListSyncRunsForUser(f.ctx, f.user.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "interrupted" || runs[0].MessagesSeen != 10 || runs[0].MessagesTotal != 25 {
		t.Fatalf("partial cleanup run=%+v, err=%v", runs, err)
	}
	f.assertFolderPresent(t, "OldFolder")
	if count, err := f.db.CountMessagesForMailbox(f.ctx, f.user.ID, mb.ID); err != nil || count != 15 {
		t.Fatalf("remaining messages=%d, err=%v", count, err)
	}
}

type cleanupRecheckFetcher struct {
	*fakeFetcher
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	result  []syncer.MailboxInfo
	err     error
}

func (f *cleanupRecheckFetcher) ListMailboxes(ctx context.Context, _ store.MailAccount) ([]syncer.MailboxInfo, error) {
	if f.calls.Add(1) == 1 {
		return []syncer.MailboxInfo{{Name: "INBOX"}}, nil
	}
	close(f.started)
	select {
	case <-f.release:
		return f.result, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestServerDeletedRunnerRechecksBeforePurging(t *testing.T) {
	for _, scenario := range []string{"listed-again", "list-error", "excluded", "purge"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServerDeletedFixture(t, "*")
			mb := f.seedFolder(t, "OldFolder", "")
			f.seedMessage(t, mb, 1)
			fetcher := &cleanupRecheckFetcher{fakeFetcher: f.fetcher, started: make(chan struct{}), release: make(chan struct{}), result: []syncer.MailboxInfo{{Name: "INBOX"}}}
			if scenario == "listed-again" {
				fetcher.result = append(fetcher.result, syncer.MailboxInfo{Name: "OldFolder"})
			}
			if scenario == "list-error" {
				fetcher.err = errors.New("LIST temporarily unavailable")
			}
			f.service.Fetcher = fetcher
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			runner := syncer.NewRunnerWithContext(ctx, f.service)
			f.discover(t)
			select {
			case <-fetcher.started:
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup worker did not start")
			}
			f.assertFolderPresent(t, "OldFolder")
			if scenario == "excluded" {
				if err := f.db.UpdateMailboxSyncMode(f.ctx, f.user.ID, mb.ID, "never"); err != nil {
					t.Fatal(err)
				}
			}
			close(fetcher.release)
			deadline := time.Now().Add(3 * time.Second)
			for runner.IsRunning(f.user.ID) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			if runner.IsRunning(f.user.ID) {
				t.Fatal("cleanup did not finish")
			}
			if scenario == "purge" {
				f.assertFolderGone(t, "OldFolder")
			} else {
				f.assertFolderPresent(t, "OldFolder")
			}
			runs, err := f.db.ListSyncRunsForUser(f.ctx, f.user.ID, 10)
			want := "ok"
			if scenario == "list-error" {
				want = "failed"
			}
			if err != nil || len(runs) != 1 || runs[0].Status != want {
				t.Fatalf("run=%+v, err=%v; want %s", runs, err, want)
			}
			if scenario == "purge" && (runs[0].MessagesSeen != 1 || runs[0].MessagesTotal != 1) {
				t.Fatalf("missing purge progress: %+v", runs[0])
			}
		})
	}
}

func TestServerDeletedFolderStillListedIsKept(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "OldFolder", "")
	f.fetcher.mailboxes = []syncer.MailboxInfo{{Name: "INBOX"}, {Name: "OldFolder"}}

	f.discover(t)

	f.assertFolderPresent(t, "OldFolder")
}

// errListFetcher fails LIST to prove a failed discovery prunes nothing.
type errListFetcher struct {
	*fakeFetcher
	listErr error
}

func (f *errListFetcher) ListMailboxes(ctx context.Context, account store.MailAccount) ([]syncer.MailboxInfo, error) {
	return nil, f.listErr
}

func TestServerDeletedListErrorPrunesNothing(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "OldFolder", "")
	f.service.Fetcher = &errListFetcher{fakeFetcher: f.fetcher, listErr: errors.New("LIST failed")}

	if _, err := f.service.DiscoverMailboxes(f.ctx, f.user.ID); err == nil {
		t.Fatal("DiscoverMailboxes with failing LIST: expected error, got nil")
	}
	f.assertFolderPresent(t, "OldFolder")
}

func TestServerDeletedExplicitPatternSkipsReconcile(t *testing.T) {
	f := newServerDeletedFixture(t, "INBOX")
	f.seedFolder(t, "OldFolder", "")

	f.discover(t)

	f.assertFolderPresent(t, "OldFolder")
}

func TestServerDeletedExemptsProtectedFolders(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "INBOX", "")
	f.seedFolder(t, "Sent", "sent")
	archive := f.seedFolder(t, "Archive", "")
	if err := f.db.UpdateMailboxSyncMode(f.ctx, f.user.ID, archive.ID, "never"); err != nil {
		t.Fatal(err)
	}
	f.seedFolder(t, "OldFolder", "")

	f.discover(t)

	for _, name := range []string{"INBOX", "Sent", "Archive"} {
		f.assertFolderPresent(t, name)
	}
	f.assertFolderGone(t, "OldFolder")
}

func TestServerDeletedPruneRemovesMessageRows(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	mb := f.seedFolder(t, "OldFolder", "")
	bl, err := f.db.CreateBlob(f.ctx, store.BlobRecord{
		UserID: f.user.ID, Kind: "message", Path: "users/1/blobs/prune-me.eml", SHA256: "abc", Size: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CreateMessage(f.ctx, store.CreateMessage{
		UserID: f.user.ID, AccountID: f.account.ID, MailboxID: mb.ID, BlobID: bl.ID,
		MessageIDHeader: "<prune-me@example.test>", Subject: "Prune me",
		UID: 1, Size: 42, BlobPath: bl.Path,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.db.CountMessagesForMailbox(f.ctx, f.user.ID, mb.ID); err != nil || got != 1 {
		t.Fatalf("messages before prune = %d, err = %v; want 1", got, err)
	}

	f.discover(t)

	if got, err := f.db.CountMessagesForMailbox(f.ctx, f.user.ID, mb.ID); err != nil || got != 0 {
		t.Fatalf("messages after prune = %d, err = %v; want 0", got, err)
	}
	f.assertFolderGone(t, "OldFolder")
}
