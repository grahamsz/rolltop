package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestMailboxGenerationBlobCleanupIsBounded(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUser(ctx, "bounded-generation-cleanup@example.test", "Cleanup", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 3, 0, 4, 1); err != nil {
		t.Fatal(err)
	}
	for uid := uint32(1); uid <= 3; uid++ {
		path := fmt.Sprintf("users/%d/blobs/accounts/%d/mailboxes/INBOX/uid-%d.eml", user.ID, account.ID, uid)
		blob, err := db.CreateBlob(ctx, store.BlobRecord{
			UserID: user.ID, Kind: "message", Path: path,
			SHA256: fmt.Sprintf("%064x", uid), Size: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.CreateMessage(ctx, store.CreateMessage{
			UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
			MessageIDHeader: fmt.Sprintf("<cleanup-%d@example.test>", uid), Subject: "Cleanup",
			Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: uid, UIDValidity: 1,
			Size: blob.Size, BlobPath: blob.Path, BodyText: "body",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, account.ID, mailbox.ID, 2, 4); err != nil || !reset {
		t.Fatalf("generation reset=%t err=%v, want true/nil", reset, err)
	}

	service := &Service{Store: db}
	if err := service.cleanupMailboxGenerationBlobs(ctx, user.ID, account.ID, mailbox.ID, 2); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.ListMailboxGenerationBlobCleanup(ctx, user.ID, account.ID, mailbox.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("cleanup queue after bounded batch=%d, want 1", len(remaining))
	}
	if err := service.cleanupMailboxGenerationBlobs(ctx, user.ID, account.ID, mailbox.ID, 2); err != nil {
		t.Fatal(err)
	}
	remaining, err = db.ListMailboxGenerationBlobCleanup(ctx, user.ID, account.ID, mailbox.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("cleanup queue after second bounded batch=%d, want 0", len(remaining))
	}
}
