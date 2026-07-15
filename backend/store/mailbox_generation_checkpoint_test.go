package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerationBoundCheckpointRejectsResetAndReusedUID(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)
	const oldGeneration uint32 = 101
	const newGeneration uint32 = 202
	const reusedUID uint32 = 7
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, reusedUID+1, oldGeneration); err != nil {
		t.Fatal(err)
	}
	oldMessage, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<old-generation@example.test>", Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: reusedUID, UIDValidity: int64(oldGeneration), Size: blob.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, reusedUID, oldGeneration)
	if err != nil || !exists {
		t.Fatalf("old-generation existence=%t err=%v, want true", exists, err)
	}

	// Interleave a reset after the existence check but before checkpoint
	// advancement, then reuse the same UID in the new generation.
	stale, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, newGeneration)
	if err != nil || !reset || len(stale) != 1 || stale[0].ID != oldMessage.ID {
		t.Fatalf("reset=%t stale=%+v err=%v", reset, stale, err)
	}
	newMessage, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<new-generation@example.test>", Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: reusedUID, UIDValidity: int64(newGeneration), Size: blob.Size,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxLastUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, reusedUID, oldGeneration); !errors.Is(err, ErrMailboxGenerationChanged) {
		t.Fatalf("stale checkpoint error=%v, want ErrMailboxGenerationChanged", err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != int64(newGeneration) || mailbox.LastUID != 0 {
		t.Fatalf("new mailbox generation=%d last_uid=%d, want %d/0", mailbox.UIDValidity, mailbox.LastUID, newGeneration)
	}
	if exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, reusedUID, oldGeneration); exists || !errors.Is(err, ErrMailboxGenerationChanged) {
		t.Fatalf("stale-generation existence=%t err=%v", exists, err)
	}
	if exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, account.ID, mailbox.ID, reusedUID, newGeneration); err != nil || !exists {
		t.Fatalf("new-generation existence=%t err=%v, message=%d", exists, err, newMessage.ID)
	}
	if err := db.UpdateMailboxRemoteStatusForGeneration(ctx, user.ID, account.ID, mailbox.ID, 9, 4, 10, oldGeneration); !errors.Is(err, ErrMailboxGenerationChanged) {
		t.Fatalf("stale status error=%v, want ErrMailboxGenerationChanged", err)
	}
	otherAccount, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: "other-checkpoint@example.test", Host: "imap.other.test", Port: 993,
		Username: "other-checkpoint", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxLastUIDForGeneration(ctx, user.ID, otherAccount.ID, mailbox.ID, reusedUID, newGeneration); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account checkpoint error=%v, want ErrNotFound", err)
	}
	if exists, err := db.MessageExistsByUIDForGeneration(ctx, user.ID, otherAccount.ID, mailbox.ID, reusedUID, newGeneration); exists || !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account existence=%t err=%v, want not found", exists, err)
	}
}

func TestInitializeMailboxRemoteStatusCannotReplaceEstablishedGeneration(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, _ := testMailbox(t, ctx, db)
	if err := db.InitializeMailboxRemoteStatus(ctx, user.ID, account.ID, mailbox.ID, 7, 3, 11, 41); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 41 || mailbox.RemoteMessageCount != 7 || mailbox.RemoteUnreadCount != 3 || mailbox.RemoteUIDNext != 11 {
		t.Fatalf("initialized mailbox=%+v", mailbox)
	}
	if err := db.InitializeMailboxRemoteStatus(ctx, user.ID, account.ID, mailbox.ID, 99, 88, 100, 42); !errors.Is(err, ErrMailboxGenerationChanged) {
		t.Fatalf("replacement initialization error=%v, want ErrMailboxGenerationChanged", err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 41 || mailbox.RemoteMessageCount != 7 || mailbox.RemoteUnreadCount != 3 || mailbox.RemoteUIDNext != 11 {
		t.Fatalf("replacement initialization mutated mailbox=%+v", mailbox)
	}
}
