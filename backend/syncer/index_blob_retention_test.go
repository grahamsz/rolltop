package syncer_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestRepairMailboxSearchIndexRetainsHydratedRawInsideConfiguredWindow(t *testing.T) {
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

	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.CreateUser(ctx, "reindex-retention@example.test", "Reindex Retention", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerAccount, err := db.UpsertMailAccount(ctx, account(owner.ID, encrypted))
	if err != nil {
		t.Fatal(err)
	}
	ownerMailbox, err := db.GetOrCreateMailbox(ctx, owner.ID, ownerAccount.ID, "Gmail Forward")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, owner.ID, ownerMailbox.ID, 2, 0, 43, 1); err != nil {
		t.Fatal(err)
	}

	retention := 5 * 365 * 24 * time.Hour
	recentRaw := []byte(rawMessage("recent@example.test", "Recent retained", "recent-full-body-needle", false))
	oldRaw := []byte(rawMessage("old@example.test", "Old ephemeral", "old-full-body-needle", false))
	recent := createRemoteOnlyMessage(t, ctx, db, owner.ID, ownerAccount.ID, ownerMailbox.ID, 41, time.Now().UTC().Add(-24*time.Hour))
	old := createRemoteOnlyMessage(t, ctx, db, owner.ID, ownerAccount.ID, ownerMailbox.ID, 42, time.Now().UTC().Add(-retention-time.Hour))

	other, err := db.CreateUser(ctx, "other-reindex@example.test", "Other Reindex", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.UpsertMailAccount(ctx, account(other.ID, encrypted))
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "Gmail Forward")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, other.ID, otherMailbox.ID, 1, 0, 44, 1); err != nil {
		t.Fatal(err)
	}
	otherMessage := createRemoteOnlyMessage(t, ctx, db, other.ID, otherAccount.ID, otherMailbox.ID, 43, time.Now().UTC().Add(-24*time.Hour))

	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		owner.ID: {
			{Mailbox: ownerMailbox.Name, UID: recent.UID, UIDValidity: 1, InternalDate: recent.InternalDate, Raw: recentRaw},
			{Mailbox: ownerMailbox.Name, UID: old.UID, UIDValidity: 1, InternalDate: old.InternalDate, Raw: oldRaw},
		},
		other.ID: {{Mailbox: otherMailbox.Name, UID: otherMessage.UID, UIDValidity: 1, InternalDate: otherMessage.InternalDate, Raw: []byte(rawMessage("other@example.test", "Other", "other-full-body-needle", false))}},
	}}
	service := &syncer.Service{
		Store: db, Blobs: blob.New(dir), Search: searchService, Fetcher: fetcher,
		BlobRetention: retention,
	}

	indexed, err := service.RepairMailboxSearchIndex(ctx, owner.ID, ownerMailbox, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 2 {
		t.Fatalf("indexed=%d, want 2", indexed)
	}
	recent, err = db.GetMessageForUser(ctx, owner.ID, recent.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownerBlobPrefix := "users/" + strconv.FormatInt(owner.ID, 10) + "/blobs/"
	if recent.BlobPath == "" || !strings.HasPrefix(filepath.ToSlash(recent.BlobPath), ownerBlobPrefix) {
		t.Fatalf("recent retained blob path=%q", recent.BlobPath)
	}
	recentBlob, err := db.GetBlobForUser(ctx, owner.ID, recent.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if recentBlob.Kind != "message" || recentBlob.Size != int64(len(recentRaw)) {
		t.Fatalf("recent retained blob=%+v", recentBlob)
	}
	if _, err := service.ApplyStorageRetention(ctx, retention, 100); err != nil {
		t.Fatal(err)
	}
	recent, err = db.GetMessageForUser(ctx, owner.ID, recent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recent.BlobPath == "" {
		t.Fatal("configured retention maintenance pruned the rehydrated recent blob")
	}

	old, err = db.GetMessageForUser(ctx, owner.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.BlobPath != "" {
		t.Fatalf("out-of-window message retained blob path=%q", old.BlobPath)
	}
	oldBlob, err := db.GetBlobForUser(ctx, owner.ID, old.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if oldBlob.Kind != "message-remote" || oldBlob.Size != 0 {
		t.Fatalf("out-of-window blob=%+v", oldBlob)
	}

	otherMessage, err = db.GetMessageForUser(ctx, other.ID, otherMessage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherMessage.BlobPath != "" {
		t.Fatalf("owner repair hydrated another tenant's message: %q", otherMessage.BlobPath)
	}

	// A retained repair result no longer needs IMAP for a second read. The old
	// message was indexed from the same response but remains remote-only by policy.
	service.Fetcher = nil
	gotRaw, err := service.FetchRawMessageForMessage(ctx, owner.ID, recent)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(recentRaw) {
		t.Fatal("retained raw message changed")
	}
	if _, err := service.FetchRawMessageForMessage(ctx, owner.ID, old); err == nil || !strings.Contains(err.Error(), "fetcher is not configured") {
		t.Fatalf("out-of-window local fetch error=%v, want missing fetcher", err)
	}
	if _, err := service.FetchRawMessageForMessage(ctx, other.ID, recent); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-tenant retained fetch error=%v, want not found", err)
	}

	// Zero retention means keep all raw messages indefinitely. A second repair
	// of the formerly out-of-window message must promote it to retained storage.
	service.Fetcher = fetcher
	service.BlobRetention = 0
	if err := searchService.DeleteMessage(ctx, owner.ID, old.ID); err != nil {
		t.Fatal(err)
	}
	indexed, err = service.RepairMailboxSearchIndex(ctx, owner.ID, ownerMailbox, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("unlimited-retention indexed=%d, want 1", indexed)
	}
	old, err = db.GetMessageForUser(ctx, owner.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.BlobPath == "" {
		t.Fatal("zero retention did not retain old reindex hydration")
	}
	oldBlob, err = db.GetBlobForUser(ctx, owner.ID, old.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if oldBlob.Kind != "message" || oldBlob.Size != int64(len(oldRaw)) {
		t.Fatalf("unlimited-retention blob=%+v", oldBlob)
	}
}

func TestRepairMailboxSearchIndexRemovesSavedRawWhenRetentionAttachFails(t *testing.T) {
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

	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "reindex-attach-failure@example.test", "Reindex Failure", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	mailAccount, err := db.UpsertMailAccount(ctx, account(user.ID, encrypted))
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, mailAccount.ID, "Gmail Forward")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 10, 1); err != nil {
		t.Fatal(err)
	}
	raw := []byte(rawMessage("failure@example.test", "Attach failure", "failure-body", false))
	blobStore := blob.New(dir)
	prunedBlob, err := blobStore.SaveRawMessage(user.ID, mailAccount.ID, mailbox.Name, 9, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobStore.DeleteUserBlob(user.ID, prunedBlob.Path); err != nil {
		t.Fatal(err)
	}
	message := createRemoteOnlyMessage(t, ctx, db, user.ID, mailAccount.ID, mailbox.ID, 9, time.Now().UTC().Add(-time.Hour))
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE blobs SET path = ? WHERE user_id = ? AND id = ?`, prunedBlob.Path, user.ID, message.BlobID); err != nil {
		t.Fatal(err)
	}
	originalBlob, err := db.GetBlobForUser(ctx, user.ID, message.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `CREATE TRIGGER fail_reindex_retention_attach
		BEFORE UPDATE OF blob_path ON messages
		WHEN NEW.user_id = `+strconv.FormatInt(user.ID, 10)+` AND NEW.id = `+strconv.FormatInt(message.ID, 10)+`
		BEGIN SELECT RAISE(FAIL, 'forced retained blob attach failure'); END`); err != nil {
		t.Fatal(err)
	}
	service := &syncer.Service{
		Store:  db,
		Blobs:  blobStore,
		Search: searchService,
		Fetcher: &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
			user.ID: {{Mailbox: mailbox.Name, UID: message.UID, UIDValidity: 1, InternalDate: message.InternalDate, Raw: raw}},
		}},
		BlobRetention: 24 * time.Hour,
	}
	indexed, err := service.RepairMailboxSearchIndex(ctx, user.ID, mailbox, 0, nil)
	if err == nil {
		t.Fatal("repair unexpectedly ignored the retained-blob SQLite failure")
	}
	if indexed != 0 {
		t.Fatalf("failed repair indexed=%d, want 0", indexed)
	}

	message, err = db.GetMessageForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if message.BlobPath != "" || message.BlobID != originalBlob.ID {
		t.Fatalf("failed attach changed message=%+v", message)
	}
	currentBlob, err := db.GetBlobForUser(ctx, user.ID, originalBlob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentBlob.Kind != "message-remote" || currentBlob.Path != originalBlob.Path || currentBlob.Size != 0 {
		t.Fatalf("failed attach changed placeholder=%+v", currentBlob)
	}
	files, err := filepath.Glob(filepath.Join(dir, "users", strconv.FormatInt(user.ID, 10), "blobs", "accounts", "*", "mailboxes", "*", "*.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("failed attach stranded raw files=%v", files)
	}
}

func createRemoteOnlyMessage(t *testing.T, ctx context.Context, db *store.Store, userID, accountID, mailboxID int64, uid uint32, date time.Time) store.MessageRecord {
	t.Helper()
	remoteBlob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: userID,
		Kind:   "message-remote",
		Path:   syncerRemoteTestPath(userID, accountID, uid),
		SHA256: "remote",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:       userID,
		AccountID:    accountID,
		MailboxID:    mailboxID,
		BlobID:       remoteBlob.ID,
		Subject:      "Remote-only message",
		FromAddr:     "sender@example.test",
		Date:         date,
		InternalDate: date,
		UID:          uid,
		UIDValidity:  1,
		Size:         100,
		BodyText:     "stored preview without the full body term",
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func syncerRemoteTestPath(userID, accountID int64, uid uint32) string {
	return filepath.ToSlash(filepath.Join("remote", "users", strconv.FormatInt(userID, 10), "accounts", strconv.FormatInt(accountID, 10), "messages", "uid-"+strconv.FormatUint(uint64(uid), 10)))
}
