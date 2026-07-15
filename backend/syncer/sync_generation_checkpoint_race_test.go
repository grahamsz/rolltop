package syncer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestSyncDoesNotAdvanceCheckpointAcrossConcurrentGenerationReset(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "checkpoint-race@example.test", "Checkpoint Race", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := mmcrypto.EncryptString([]byte("12345678901234567890123456789012"), "unused")
	if err != nil {
		t.Fatal(err)
	}
	accountRecord, err := db.UpsertMailAccount(ctx, account(user.ID, encrypted))
	if err != nil {
		t.Fatal(err)
	}
	oldRaw := []byte(rawMessage("old-checkpoint@example.test", "Old checkpoint", "old-checkpoint", false))
	fetcher := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{
			user.ID: {{Mailbox: "INBOX", UID: 5, InternalDate: time.Now().UTC(), Raw: oldRaw}},
		},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE mailboxes SET last_uid = 4
		WHERE user_id = ? AND account_id = ? AND id = ?`, user.ID, accountRecord.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}

	resetDone := false
	service.ScheduleInboxArrival = func(scheduledUserID, scheduledAccountID int64, _ time.Time) {
		if resetDone {
			return
		}
		resetDone = true
		if scheduledUserID != user.ID || scheduledAccountID != accountRecord.ID {
			t.Fatalf("arrival schedule scope=%d/%d, want %d/%d", scheduledUserID, scheduledAccountID, user.ID, accountRecord.ID)
		}
		if _, reset, resetErr := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, accountRecord.ID, mailbox.ID, 2); resetErr != nil || !reset {
			t.Fatalf("concurrent reset=%t err=%v", reset, resetErr)
		}
		replacementBlob, createErr := db.CreateBlob(ctx, store.BlobRecord{
			UserID: user.ID, Kind: "message-remote", Path: "replacement-generation.eml", SHA256: "replacement", Size: 11,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, createErr = db.CreateMessage(ctx, store.CreateMessage{
			UserID: user.ID, AccountID: accountRecord.ID, MailboxID: mailbox.ID, BlobID: replacementBlob.ID,
			MessageIDHeader: "<replacement-checkpoint@example.test>", Subject: "Replacement generation",
			Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: 5, UIDValidity: 2, Size: replacementBlob.Size,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
	}

	_, err = service.SyncUser(ctx, user.ID)
	if !errors.Is(err, store.ErrMailboxGenerationChanged) {
		t.Fatalf("sync error=%v, want ErrMailboxGenerationChanged", err)
	}
	if !resetDone {
		t.Fatal("test did not force the checkpoint race")
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 2 || mailbox.LastUID != 0 {
		t.Fatalf("mailbox generation=%d last_uid=%d, want 2/0", mailbox.UIDValidity, mailbox.LastUID)
	}
	replacement, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Subject != "Replacement generation" {
		t.Fatalf("reused UID message=%q", replacement.Subject)
	}
}
