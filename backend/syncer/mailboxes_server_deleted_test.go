// File overview: Tests for server-side folder deletion reconciliation.
//
// When an account uses "*" folder discovery, a completed IMAP LIST is
// authoritative: a local folder it stops reporting is pruned immediately, the
// same way reconcileMailboxUIDs drops local messages the server no longer
// reports. INBOX, special-use folders, and folders the user excluded from
// sync are exempt, and a failed LIST prunes nothing.

package syncer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
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

func TestServerDeletedFolderPrunedImmediately(t *testing.T) {
	f := newServerDeletedFixture(t, "*")
	f.seedFolder(t, "OldFolder", "")

	f.discover(t)

	f.assertFolderGone(t, "OldFolder")
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
