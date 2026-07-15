// File overview: Regression coverage for notifications after remote mailbox moves.

package syncer_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type failingNotificationMoveFetcher struct {
	*fakeFetcher
}

func (f *failingNotificationMoveFetcher) MoveMessage(context.Context, store.MailAccount, string, string, uint32) error {
	return errors.New("expected remote move failure")
}

func (f *failingNotificationMoveFetcher) MoveMessageWithReceipt(context.Context, store.MailAccount, string, string, uint32, uint32) (*syncer.MoveReceipt, error) {
	return nil, errors.New("expected remote move failure")
}

func TestMoveFromSpamToInboxDoesNotCreateNewMailEvent(t *testing.T) {
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

	user, err := db.CreateUser(ctx, "move-notification@example.test", "Move Notification", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	accountRecord, err := db.UpsertMailAccount(ctx, account(user.ID, encrypted))
	if err != nil {
		t.Fatal(err)
	}

	baselineRaw := []byte(rawMessage("baseline@example.test", "Inbox baseline", "baseline", false))
	movedRaw := []byte(rawMessage("moved@example.test", "Move this message", "moved body", false))
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {
			{Mailbox: "INBOX", UID: 1, InternalDate: now, Raw: baselineRaw},
			{Mailbox: "Spam", UID: 9, InternalDate: now.Add(time.Minute), Raw: movedRaw},
		},
	}}
	service := &syncer.Service{
		Store: db, Blobs: blob.New(dir), Search: searchService, Fetcher: fetcher,
	}
	if _, err := service.SyncUserMailboxes(ctx, user.ID, []string{"INBOX", "Spam"}); err != nil {
		t.Fatal(err)
	}
	spam, err := db.GetOrCreateMailbox(ctx, user.ID, accountRecord.ID, "Spam")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := db.GetOrCreateMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	spamMessages, err := db.ListMessagesForMailbox(ctx, user.ID, spam.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(spamMessages) != 1 {
		t.Fatalf("spam messages = %d, want 1", len(spamMessages))
	}

	if err := service.MoveMessage(ctx, user.ID, spamMessages[0].ID, inbox.ID); err != nil {
		t.Fatal(err)
	}
	fetcher.messages[user.ID] = []syncer.FetchedMessage{
		{Mailbox: "INBOX", UID: 1, InternalDate: now, Raw: baselineRaw},
		{Mailbox: "INBOX", UID: 2, InternalDate: now.Add(time.Minute), Raw: movedRaw},
	}
	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX", "Spam"}); err != nil {
		t.Fatal(err)
	}
	events, count, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || cursor != 0 || len(events) != 0 {
		t.Fatalf("move-created events = %+v count=%d cursor=%d, want none", events, count, cursor)
	}

	genuineRaw := []byte(rawMessage("new@example.test", "Actually new", "new body", false))
	fetcher.messages[user.ID] = append(fetcher.messages[user.ID], syncer.FetchedMessage{
		Mailbox: "INBOX", UID: 3, InternalDate: now.Add(2 * time.Minute), Raw: genuineRaw,
	})
	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	events, count, _, err = db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(events) != 1 || events[0].Subject != "Actually new" {
		t.Fatalf("genuine new-mail events = %+v count=%d, want one", events, count)
	}

	failedRaw := []byte(rawMessage("failed@example.test", "Move will fail", "failed body", false))
	fetcher.messages[user.ID] = append(fetcher.messages[user.ID], syncer.FetchedMessage{
		Mailbox: "Spam", UID: 10, InternalDate: now.Add(3 * time.Minute), Raw: failedRaw,
	})
	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, accountRecord.ID, []string{"Spam"}); err != nil {
		t.Fatal(err)
	}
	spamMessages, err = db.ListMessagesForMailbox(ctx, user.ID, spam.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(spamMessages) != 1 {
		t.Fatalf("spam messages after failed candidate sync = %d, want 1", len(spamMessages))
	}
	failingService := *service
	failingService.Fetcher = &failingNotificationMoveFetcher{fakeFetcher: fetcher}
	if err := failingService.MoveMessage(ctx, user.ID, spamMessages[0].ID, inbox.ID); err == nil {
		t.Fatal("remote move unexpectedly succeeded")
	}
	var pending int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_move_notifications
		WHERE user_id = ? AND consumed_message_id IS NULL`, user.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("unconsumed notification markers after failed move = %d, want 0", pending)
	}
}
