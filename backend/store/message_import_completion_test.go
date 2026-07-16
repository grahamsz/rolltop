package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPendingMessageImportIsExcludedUntilCompleted(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)
	const generation uint32 = 707
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 2, 0, 3, generation); err != nil {
		t.Fatal(err)
	}
	completed := createImportCompletionTestMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob, 1, generation, false)
	pending := createImportCompletionTestMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob, 2, generation, true)

	if got := messageImportCompletedAt(t, ctx, db, user.ID, completed.ID); got <= 0 {
		t.Fatalf("default message import_completed_at=%d, want completed timestamp", got)
	}
	if got := messageImportCompletedAt(t, ctx, db, user.ID, pending.ID); got != 0 {
		t.Fatalf("pending message import_completed_at=%d, want 0", got)
	}
	if exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, 1, generation); err != nil || !exists {
		t.Fatalf("completed generation existence=%t err=%v, want true", exists, err)
	}
	if exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, 2, generation); err != nil || exists {
		t.Fatalf("pending generation existence=%t err=%v, want false", exists, err)
	}
	if uids, err := db.MessageUIDsForMailbox(ctx, user.ID, account.ID, mailbox.ID); err != nil || !slices.Equal(uids, []uint32{1}) {
		t.Fatalf("UID inventory before completion=%v err=%v, want [1]", uids, err)
	}

	if err := db.MarkMessageImportCompleted(ctx, user.ID, pending.ID); err != nil {
		t.Fatal(err)
	}
	if exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, 2, generation); err != nil || !exists {
		t.Fatalf("marked generation existence=%t err=%v, want true", exists, err)
	}
	if uids, err := db.MessageUIDsForMailbox(ctx, user.ID, account.ID, mailbox.ID); err != nil || !slices.Equal(uids, []uint32{1, 2}) {
		t.Fatalf("UID inventory after completion=%v err=%v, want [1 2]", uids, err)
	}
}

func TestCreateMessageCompletionIsMonotonicAcrossRetries(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)

	pending := createImportCompletionTestMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob, 9, 0, true)
	if got := messageImportCompletedAt(t, ctx, db, user.ID, pending.ID); got != 0 {
		t.Fatalf("initial import_completed_at=%d, want 0", got)
	}
	completed := createImportCompletionTestMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob, 9, 0, false)
	if completed.ID != pending.ID {
		t.Fatalf("retry message ID=%d, want existing %d", completed.ID, pending.ID)
	}
	completedAt := messageImportCompletedAt(t, ctx, db, user.ID, pending.ID)
	if completedAt <= 0 {
		t.Fatalf("promoted import_completed_at=%d, want completed timestamp", completedAt)
	}
	createImportCompletionTestMessage(t, ctx, db, user.ID, account.ID, mailbox.ID, blob, 9, 0, true)
	if got := messageImportCompletedAt(t, ctx, db, user.ID, pending.ID); got != completedAt {
		t.Fatalf("pending retry regressed import_completed_at from %d to %d", completedAt, got)
	}
}

func TestMarkMessagesImportCompletedIgnoresMissingAndForeignRows(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, account, mailbox, blob := testMailbox(t, ctx, db)
	owned := createImportCompletionTestMessage(t, ctx, db, owner.ID, account.ID, mailbox.ID, blob, 21, 0, true)

	other, err := db.CreateUser(ctx, "other-import@example.test", "Other import", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: other.ID, Email: other.Email, Host: "imap.other.example.test", Port: 993,
		Username: "other-import", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	otherBlob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: other.ID, Kind: "message", Path: "users/2/blobs/import-pending.eml", SHA256: "other-import", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := createImportCompletionTestMessage(t, ctx, db, other.ID, otherAccount.ID, otherMailbox.ID, otherBlob, 22, 0, true)

	if err := db.MarkMessagesImportCompleted(ctx, owner.ID, []int64{owned.ID, owned.ID, foreign.ID, 999_999}); err != nil {
		t.Fatalf("mark with moved/missing rows: %v", err)
	}
	if got := messageImportCompletedAt(t, ctx, db, owner.ID, owned.ID); got <= 0 {
		t.Fatalf("owned import_completed_at=%d, want completed timestamp", got)
	}
	if got := messageImportCompletedAt(t, ctx, db, other.ID, foreign.ID); got != 0 {
		t.Fatalf("foreign import_completed_at=%d, want unchanged 0", got)
	}
	if err := db.MarkMessageImportCompleted(ctx, owner.ID, 999_999); err != nil {
		t.Fatalf("deleted source row should be terminal: %v", err)
	}
}

func createImportCompletionTestMessage(t *testing.T, ctx context.Context, db *Store, userID, accountID, mailboxID int64, blob BlobRecord, uid, uidValidity uint32, pending bool) MessageRecord {
	t.Helper()
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: userID, AccountID: accountID, MailboxID: mailboxID, BlobID: blob.ID,
		MessageIDHeader: "<import-completion@example.test>", Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: uid, UIDValidity: int64(uidValidity), Size: blob.Size, BlobPath: blob.Path, ImportPending: pending,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func messageImportCompletedAt(t *testing.T, ctx context.Context, db *Store, userID, messageID int64) int64 {
	t.Helper()
	dataDB, err := db.dataDB(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	var completedAt int64
	if err := dataDB.QueryRowContext(ctx, `SELECT import_completed_at FROM messages WHERE user_id = ? AND id = ?`, userID, messageID).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	return completedAt
}
