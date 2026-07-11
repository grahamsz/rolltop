package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestNewMailEventsAreIdempotentAndUserScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "events-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "events-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createNewMailEventMessage(t, ctx, db, owner, 41, "Alice <alice@example.test>", "Hello")
	otherMessage := createNewMailEventMessage(t, ctx, db, other, 41, "", "Other")
	first, created, err := db.RecordNewMailEvent(ctx, owner.ID, message)
	if err != nil || !created {
		t.Fatalf("first event = %+v created=%t err=%v", first, created, err)
	}
	duplicate, created, err := db.RecordNewMailEvent(ctx, owner.ID, message)
	if err != nil || created || duplicate.ID != first.ID {
		t.Fatalf("duplicate event = %+v created=%t err=%v", duplicate, created, err)
	}
	if _, _, err := db.RecordNewMailEvent(ctx, other.ID, message); err == nil {
		t.Fatal("cross-user event insert succeeded")
	}
	if _, _, err := db.RecordNewMailEvent(ctx, other.ID, otherMessage); err != nil {
		t.Fatal(err)
	}

	events, count, cursor, err := db.NewMailEventsAfter(ctx, owner.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || cursor != first.ID || len(events) != 1 || events[0].UserID != owner.ID || events[0].Subject != "Hello" {
		t.Fatalf("owner events = %+v count=%d cursor=%d", events, count, cursor)
	}
	for _, event := range events {
		if event.ID > cursor {
			t.Fatalf("event id %d escaped response cursor %d", event.ID, cursor)
		}
	}
	otherEvents, otherCount, _, err := db.NewMailEventsAfter(ctx, other.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if otherCount != 1 || len(otherEvents) != 1 || otherEvents[0].UserID != other.ID || otherEvents[0].Subject != "Other" {
		t.Fatalf("other events = %+v count=%d", otherEvents, otherCount)
	}
	after, afterCount, afterCursor, err := db.NewMailEventsAfter(ctx, owner.ID, cursor, 5)
	if err != nil {
		t.Fatal(err)
	}
	if afterCount != 0 || len(after) != 0 || afterCursor != cursor {
		t.Fatalf("events after cursor = %+v count=%d cursor=%d", after, afterCount, afterCursor)
	}
	if err := db.DeleteMessageForUser(ctx, owner.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	deletedEvents, deletedCount, deletedCursor, err := db.NewMailEventsAfter(ctx, owner.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if deletedCount != 0 || len(deletedEvents) != 0 || deletedCursor != 0 {
		t.Fatalf("events after message delete = %+v count=%d cursor=%d", deletedEvents, deletedCount, deletedCursor)
	}
}

func createNewMailEventMessage(t *testing.T, ctx context.Context, db *Store, user User, uid uint32, from, subject string) MessageRecord {
	t.Helper()
	account, err := db.UpsertMailAccount(ctx, MailAccount{
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
	blob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID, Kind: "message", Path: fmt.Sprintf("users/%d/events-%d.eml", user.ID, uid),
		SHA256: fmt.Sprintf("events-%d-%d", user.ID, uid), Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		FromAddr: from, Subject: subject, Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
		UID: uid, BlobPath: blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}
