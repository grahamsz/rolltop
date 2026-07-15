package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestMailboxGenerationUpgradeLifecycleSurvivesRestartWithoutMessageState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "no-state")
	message := createMailboxGenerationLifecycleMessage(t, db, user.ID, account.ID, mailbox.ID, 1, 9001, "no-state")
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 2, 9001); err != nil {
		t.Fatal(err)
	}
	// Model a row created before schema 022, when message generation was not
	// persisted even though the mailbox checkpoint already had UIDVALIDITY.
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET uid_validity = 0 WHERE user_id = ? AND id = ?`, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 9001); err != nil || !reset {
		t.Fatalf("reset=%v err=%v, want true/nil", reset, err)
	}

	assertMailboxGenerationLifecycle(t, db, user.ID, account.ID, mailbox.ID, 9001, true, 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertMailboxGenerationLifecycle(t, db, user.ID, account.ID, mailbox.ID, 9001, true, 0)
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, account.ID, mailbox.ID, 9001); err != nil {
		t.Fatal(err)
	}
	assertMailboxGenerationLifecycle(t, db, user.ID, account.ID, mailbox.ID, 9001, false, 0)
}

func TestMailboxGenerationBlobCleanupRetriesFilesystemFailure(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "cleanup-retry")
	message := createMailboxGenerationLifecycleMessage(t, db, user.ID, account.ID, mailbox.ID, 1, 1, "cleanup-retry")
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 2); err != nil || !reset {
		t.Fatalf("reset=%v err=%v, want true/nil", reset, err)
	}
	queued := listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)
	if len(queued) != 1 || queued[0].BlobID != message.BlobID {
		t.Fatalf("queued cleanup=%+v, want blob %d", queued, message.BlobID)
	}

	deleteErr := errors.New("injected filesystem failure")
	if err := db.CompleteMailboxGenerationBlobCleanup(ctx, user.ID, queued[0].ID, func(string) error { return deleteErr }); !errors.Is(err, deleteErr) {
		t.Fatalf("cleanup error=%v, want %v", err, deleteErr)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, message.BlobID); err != nil {
		t.Fatalf("failed cleanup removed blob metadata: %v", err)
	}
	if got := len(listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)); got != 1 {
		t.Fatalf("failed cleanup retained %d queue rows, want 1", got)
	}

	var deletedPath string
	if err := db.CompleteMailboxGenerationBlobCleanup(ctx, user.ID, queued[0].ID, func(path string) error {
		deletedPath = path
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deletedPath != message.BlobPath {
		t.Fatalf("deleted path=%q, want %q", deletedPath, message.BlobPath)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, message.BlobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blob lookup error=%v, want not found", err)
	}
	if got := len(listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)); got != 0 {
		t.Fatalf("completed cleanup retained %d queue rows", got)
	}
}

func TestMailboxGenerationBlobCleanupProtectsReusedBlob(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "cleanup-reuse")
	message := createMailboxGenerationLifecycleMessage(t, db, user.ID, account.ID, mailbox.ID, 1, 1, "cleanup-reuse")
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 2); err != nil || !reset {
		t.Fatalf("reset=%v err=%v, want true/nil", reset, err)
	}
	queued := listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)
	if len(queued) != 1 {
		t.Fatalf("queued cleanup=%+v, want one row", queued)
	}

	reused, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: message.BlobID,
		MessageIDHeader: "<cleanup-reuse-refetched@example.test>", Subject: "cleanup-reuse-refetched",
		Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: 1, UIDValidity: 2,
		Size: message.Size, BlobPath: message.BlobPath, BodyText: "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteMailboxGenerationBlobCleanup(ctx, user.ID, queued[0].ID, func(string) error {
		t.Fatal("filesystem deletion ran for a reattached blob")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetBlobForUser(ctx, user.ID, message.BlobID); err != nil {
		t.Fatalf("reused blob metadata was removed: %v", err)
	}
	if _, err := db.GetMessageForUser(ctx, user.ID, reused.ID); err != nil {
		t.Fatalf("message reusing blob was removed: %v", err)
	}
	if got := len(listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)); got != 0 {
		t.Fatalf("protected cleanup retained %d queue rows", got)
	}
}

func TestMailboxGenerationBlobCleanupFinishesAfterMetadataLoss(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox := createMailboxGenerationLifecycleFixture(t, db, "cleanup-metadata-loss")
	message := createMailboxGenerationLifecycleMessage(t, db, user.ID, account.ID, mailbox.ID, 1, 1, "cleanup-metadata-loss")
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 2); err != nil || !reset {
		t.Fatalf("reset=%v err=%v, want true/nil", reset, err)
	}
	queued := listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)
	if len(queued) != 1 {
		t.Fatalf("queued cleanup=%+v, want one row", queued)
	}
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM blobs WHERE user_id = ? AND id = ?`, user.ID, message.BlobID); err != nil {
		t.Fatal(err)
	}
	var deletedPath string
	if err := db.CompleteMailboxGenerationBlobCleanup(ctx, user.ID, queued[0].ID, func(path string) error {
		deletedPath = path
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deletedPath != message.BlobPath {
		t.Fatalf("deleted path=%q, want %q", deletedPath, message.BlobPath)
	}
	if got := len(listMailboxGenerationBlobCleanupForTest(t, db, user.ID, account.ID, mailbox.ID)); got != 0 {
		t.Fatalf("metadata-loss cleanup retained %d queue rows", got)
	}
}

func createMailboxGenerationLifecycleFixture(t *testing.T, db *Store, suffix string) (User, MailAccount, Mailbox) {
	t.Helper()
	ctx := context.Background()
	user, err := db.CreateUser(ctx, fmt.Sprintf("generation-%s@example.test", suffix), suffix, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: suffix, EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	return user, account, mailbox
}

func createMailboxGenerationLifecycleMessage(t *testing.T, db *Store, userID, accountID, mailboxID int64, uid, uidValidity uint32, suffix string) MessageRecord {
	t.Helper()
	ctx := context.Background()
	if err := db.UpdateMailboxRemoteStatus(ctx, userID, mailboxID, 1, 0, uid+1, uidValidity); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("users/%d/blobs/accounts/%d/mailboxes/INBOX/uid-%d-%s.eml", userID, accountID, uid, suffix)
	blob, err := db.CreateBlob(ctx, BlobRecord{UserID: userID, Kind: "message", Path: path, SHA256: suffix, Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: userID, AccountID: accountID, MailboxID: mailboxID, BlobID: blob.ID,
		MessageIDHeader: fmt.Sprintf("<%s@example.test>", suffix), Subject: suffix,
		Date: now, InternalDate: now, UID: uid, UIDValidity: int64(uidValidity),
		Size: 4, BlobPath: path, BodyText: "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func listMailboxGenerationBlobCleanupForTest(t *testing.T, db *Store, userID, accountID, mailboxID int64) []MailboxGenerationBlobCleanup {
	t.Helper()
	items, err := db.ListMailboxGenerationBlobCleanup(context.Background(), userID, accountID, mailboxID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func assertMailboxGenerationLifecycle(t *testing.T, db *Store, userID, accountID, mailboxID int64, uidValidity uint32, wantPending bool, wantMessageRows int) {
	t.Helper()
	pending, err := db.MailboxGenerationRebuildPending(context.Background(), userID, accountID, mailboxID, uidValidity)
	if err != nil {
		t.Fatal(err)
	}
	if pending != wantPending {
		t.Fatalf("rebuild pending=%v, want %v", pending, wantPending)
	}
	var rows int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, userID, accountID, mailboxID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != wantMessageRows {
		t.Fatalf("rebuild message rows=%d, want %d", rows, wantMessageRows)
	}
}
