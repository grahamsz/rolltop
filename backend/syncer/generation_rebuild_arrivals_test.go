package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestReplayStoredGenerationRebuildArrivalCancelsNonInboxSnoozeIdempotently(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUser(ctx, "generation-arrival-snooze@example.test", "Generation arrival", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 2, 0, 5, 2); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}

	createMessage := func(uid uint32, suffix string) store.MessageRecord {
		t.Helper()
		path := fmt.Sprintf("users/%d/blobs/archive-%s.eml", user.ID, suffix)
		blob, err := db.CreateBlob(ctx, store.BlobRecord{
			UserID: user.ID, Kind: "message", Path: path, SHA256: suffix, Size: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		message, err := db.CreateMessage(ctx, store.CreateMessage{
			UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
			MessageIDHeader: fmt.Sprintf("<%s@example.test>", suffix), ThreadKey: "shared-thread",
			Subject: suffix, Date: now, InternalDate: now, UID: uid, UIDValidity: 2,
			Size: 4, BlobPath: path, BodyText: "body",
		})
		if err != nil {
			t.Fatal(err)
		}
		return message
	}

	root := createMessage(1, "root")
	if _, err := db.SnoozeMessage(ctx, user.ID, root.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, arrival_uid_floor, created_at, updated_at)
		VALUES (?, ?, ?, 2, 4, 1, 1)`, user.ID, account.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}
	arrival := createMessage(4, "arrival")

	service := &Service{Store: db}
	for attempt := 0; attempt < 2; attempt++ {
		if err := service.replayStoredGenerationRebuildArrivals(ctx, user.ID, account, mailbox,
			2, 4, 0, &store.SyncProgress{}); err != nil {
			t.Fatalf("replay attempt %d: %v", attempt+1, err)
		}
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, arrival.ID); err == nil {
		t.Fatal("post-floor Archive arrival did not cancel the conversation snooze")
	}
	var events int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM new_mail_events WHERE user_id = ?`, user.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("non-Inbox replay created %d new-mail events", events)
	}
}
