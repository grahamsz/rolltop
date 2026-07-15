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

type interruptingGenerationFetcher struct {
	*fakeFetcher
	stopAfter int
	err       error
}

func (f *interruptingGenerationFetcher) FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount, mailbox string, afterUID, expectedUIDValidity uint32, handle func(syncer.FetchedMessage) error) error {
	handled := 0
	return f.fakeFetcher.FetchMailboxWithUIDValidity(ctx, account, mailbox, afterUID, expectedUIDValidity, func(item syncer.FetchedMessage) error {
		if err := handle(item); err != nil {
			return err
		}
		handled++
		if handled == f.stopAfter {
			return f.err
		}
		return nil
	})
}

func TestGenerationRebuildResumesWithoutRepairNotificationOrSnoozeCancellation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rolltop.db")
	searchPath := filepath.Join(dir, "bleve")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	searchService, err := search.Open(searchPath)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "generation-resume@example.test", "Generation Resume", "hash", false)
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
	now := time.Date(2026, time.July, 14, 18, 0, 0, 0, time.UTC)
	rootRaw := []byte("From: Root <root@example.test>\r\nTo: archive@example.test\r\nSubject: Root\r\nDate: Tue, 14 Jul 2026 18:00:00 +0000\r\nMessage-ID: <generation-root@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nroot\r\n")
	replyRaw := []byte("From: Reply <reply@example.test>\r\nTo: archive@example.test\r\nSubject: Re: Root\r\nDate: Tue, 14 Jul 2026 18:01:00 +0000\r\nMessage-ID: <generation-reply@example.test>\r\nIn-Reply-To: <generation-root@example.test>\r\nReferences: <generation-root@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nreply\r\n")
	remoteMessages := []syncer.FetchedMessage{
		{Mailbox: "INBOX", UID: 1, InternalDate: now, Raw: rootRaw},
		{Mailbox: "INBOX", UID: 2, InternalDate: now.Add(time.Minute), Raw: replyRaw},
	}
	initialFetcher := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: remoteMessages},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchService, Fetcher: initialFetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	rootMessage, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	snooze, err := db.SnoozeMessage(ctx, user.ID, rootMessage.ID, now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	crashErr := errors.New("injected fetch interruption")
	crashBase := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: remoteMessages},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	service.Fetcher = &interruptingGenerationFetcher{fakeFetcher: crashBase, stopAfter: 1, err: crashErr}
	if _, err := service.SyncUser(ctx, user.ID); !errors.Is(err, crashErr) {
		t.Fatalf("interrupted sync error=%v, want %v", err, crashErr)
	}
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || !pending {
		t.Fatalf("rebuild pending=%v err=%v, want true/nil", pending, err)
	}
	var messageJournalRows int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, user.ID, accountRecord.ID, mailbox.ID).Scan(&messageJournalRows); err != nil {
		t.Fatal(err)
	}
	if messageJournalRows != 0 {
		t.Fatalf("partial rebuild retained %d per-message state rows after restoring UID 1, want 0", messageJournalRows)
	}
	if err := searchService.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err = search.Open(searchPath)
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()
	resumeFetcher := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: remoteMessages},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 2},
	}
	service = &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchService, Fetcher: resumeFetcher}
	if _, err := service.SyncUserMailboxes(ctx, user.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if len(resumeFetcher.fetchUIDCalls) != 0 {
		t.Fatalf("resume used sparse requested repair calls=%v", resumeFetcher.fetchUIDCalls)
	}
	if len(resumeFetcher.fetchAfterUIDs) == 0 || resumeFetcher.fetchAfterUIDs[0] != 1 {
		t.Fatalf("resume checkpoints=%v, want first fetch after UID 1", resumeFetcher.fetchAfterUIDs)
	}
	pending, err = db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || pending {
		t.Fatalf("completed rebuild pending=%v err=%v, want false/nil", pending, err)
	}
	restoredRoot, err := db.GetMessageByUID(ctx, user.ID, accountRecord.ID, mailbox.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	restoredSnooze, err := db.MessageSnoozeForUser(ctx, user.ID, restoredRoot.ID)
	if err != nil {
		t.Fatalf("generation backfill reply cancelled restored snooze: %v", err)
	}
	if restoredSnooze.ID != snooze.ID || restoredSnooze.Generation != snooze.Generation {
		t.Fatalf("restored snooze=%+v, want id=%d generation=%d", restoredSnooze, snooze.ID, snooze.Generation)
	}
	events, eventCount, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || cursor != 0 || len(events) != 0 {
		t.Fatalf("generation backfill emitted new-mail events=%+v count=%d cursor=%d", events, eventCount, cursor)
	}
}
