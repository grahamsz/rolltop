// File overview: Tenant isolation tests for sync, blobs, search, and message operations.

package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type fakeFetcher struct {
	messages      map[int64][]syncer.FetchedMessage
	mailboxes     []syncer.MailboxInfo
	calls         []int64
	fetchUIDCalls [][]uint32
	appendDate    time.Time
}

type cancelingFetcher struct {
	*fakeFetcher
	cancel context.CancelFunc
}

func TestNewMailEventsExcludeInitialInboxImport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	user, err := db.CreateUser(ctx, "notification-events@example.test", "Notifications", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user.ID, encrypted)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {{Mailbox: "INBOX", UID: 1, InternalDate: now, Raw: []byte(rawMessage("first@example.test", "Initial import", "first", false))}},
	}}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	initial, count, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || cursor != 0 || len(initial) != 0 {
		t.Fatalf("initial import events = %+v count=%d cursor=%d", initial, count, cursor)
	}

	fetcher.messages[user.ID] = append(fetcher.messages[user.ID], syncer.FetchedMessage{
		Mailbox: "INBOX", UID: 2, InternalDate: now.Add(time.Minute),
		Raw: []byte(rawMessage("second@example.test", "Incremental arrival", "second", false)),
	})
	run, err := service.SyncUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, count, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if run.NewMessages != 1 || count != 1 || cursor == 0 || len(events) != 1 || events[0].Subject != "Incremental arrival" {
		t.Fatalf("incremental run=%+v events=%+v count=%d cursor=%d", run, events, count, cursor)
	}

	boxes, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil || len(boxes) != 1 {
		t.Fatalf("mailboxes = %+v err=%v", boxes, err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `DELETE FROM new_mail_events WHERE user_id = ?`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE mailboxes SET last_uid = 1 WHERE user_id = ? AND id = ?`, user.ID, boxes[0].ID); err != nil {
		t.Fatal(err)
	}
	recoveredRun, err := service.SyncUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRun, err = db.GetSyncRunForUser(ctx, user.ID, recoveredRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, recoveredCount, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredRun.NewMessages != 1 || recoveredRun.MessagesSkipped != 1 || recoveredCount != 1 || len(recovered) != 1 || recovered[0].Subject != "Incremental arrival" {
		t.Fatalf("recovery run=%+v events=%+v count=%d", recoveredRun, recovered, recoveredCount)
	}
}

func TestNewMailEventsIncludeFirstArrivalAfterEmptyInboxSync(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	user, err := db.CreateUser(ctx, "empty-inbox@example.test", "Empty Inbox", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user.ID, encrypted)); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{user.ID: {}}}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	fetcher.messages[user.ID] = []syncer.FetchedMessage{{
		Mailbox: "INBOX", UID: 1, InternalDate: time.Now().UTC(),
		Raw: []byte(rawMessage("first@example.test", "First after empty", "first", false)),
	}}
	run, err := service.SyncUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, count, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if run.NewMessages != 1 || count != 1 || cursor == 0 || len(events) != 1 || events[0].Subject != "First after empty" {
		t.Fatalf("first arrival run=%+v events=%+v count=%d cursor=%d", run, events, count, cursor)
	}
}

func TestIncrementalNonInboxArrivalCancelsConversationSnoozeWithoutNewMailEvent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	user, err := db.CreateUser(ctx, "archive-snooze@example.test", "Archive", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	mailAccount := account(user.ID, encrypted)
	mailAccount.Mailbox = "*"
	if _, err := db.UpsertMailAccount(ctx, mailAccount); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fetcher := &fakeFetcher{
		mailboxes: []syncer.MailboxInfo{{Name: "Archive"}},
		messages: map[int64][]syncer.FetchedMessage{user.ID: {{
			Mailbox: "Archive", UID: 1, InternalDate: now,
			Raw: []byte(rawMessage("first@example.test", "Archive root", "first", false)),
		}}},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUserMailboxes(ctx, user.ID, []string{"Archive"}); err != nil {
		t.Fatal(err)
	}
	boxes, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil || len(boxes) != 1 {
		t.Fatalf("mailboxes = %+v err=%v", boxes, err)
	}
	messages, err := db.ListMessagesForMailbox(ctx, user.ID, boxes[0].ID, 10, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("initial messages = %+v err=%v", messages, err)
	}
	if _, err := db.SnoozeMessage(ctx, user.ID, messages[0].ID, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fetcher.messages[user.ID] = append(fetcher.messages[user.ID], syncer.FetchedMessage{
		Mailbox: "Archive", UID: 2, InternalDate: now.Add(time.Minute),
		Raw: []byte("From: second@example.test\r\nTo: archive@example.test\r\nSubject: Re: Archive root\r\nDate: Fri, 01 May 2026 12:01:00 +0000\r\nMessage-ID: <archive-reply@example.test>\r\nReferences: <Archive-root@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsecond\r\n"),
	})
	if _, err := service.SyncUserMailboxes(ctx, user.ID, []string{"Archive"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, messages[0].ID); err == nil {
		t.Fatal("incremental Archive reply left its conversation snoozed")
	}
	events, count, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil || count != 0 || cursor != 0 || len(events) != 0 {
		t.Fatalf("non-Inbox arrival emitted new-mail events = %+v count=%d cursor=%d err=%v", events, count, cursor, err)
	}
}

func (f *cancelingFetcher) FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(syncer.FetchedMessage) error) error {
	f.cancel()
	return ctx.Err()
}

func (f *fakeFetcher) ListMailboxes(ctx context.Context, account store.MailAccount) ([]syncer.MailboxInfo, error) {
	if len(f.mailboxes) > 0 {
		return append([]syncer.MailboxInfo(nil), f.mailboxes...), nil
	}
	return []syncer.MailboxInfo{{Name: "INBOX"}}, nil
}

func (f *fakeFetcher) MailboxStatus(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxStatus, error) {
	var highest uint32
	var count uint32
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox != mailbox {
			continue
		}
		count++
		if msg.UID > highest {
			highest = msg.UID
		}
	}
	return syncer.MailboxStatus{Messages: count, UIDNext: highest + 1}, nil
}

func (f *fakeFetcher) UIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	var uids []uint32
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox == mailbox {
			uids = append(uids, msg.UID)
		}
	}
	return uids, nil
}

func (f *fakeFetcher) FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(syncer.FetchedMessage) error) error {
	f.calls = append(f.calls, account.UserID)
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox == mailbox && msg.UID > afterUID {
			if err := handle(msg); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeFetcher) FetchUIDs(ctx context.Context, account store.MailAccount, mailbox string, uids []uint32, handle func(syncer.FetchedMessage) error) error {
	f.fetchUIDCalls = append(f.fetchUIDCalls, append([]uint32(nil), uids...))
	wanted := map[uint32]bool{}
	for _, uid := range uids {
		wanted[uid] = true
	}
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox == mailbox && wanted[msg.UID] {
			if err := handle(msg); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeFetcher) FetchMessage(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (syncer.FetchedMessage, error) {
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox == mailbox && msg.UID == uid {
			return msg, nil
		}
	}
	return syncer.FetchedMessage{}, store.ErrNotFound
}

func (f *fakeFetcher) AppendMessage(ctx context.Context, account store.MailAccount, mailbox string, raw []byte, messageID string, date time.Time) (syncer.FetchedMessage, error) {
	var highest uint32
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox == mailbox && msg.UID > highest {
			highest = msg.UID
		}
	}
	internalDate := date
	if !f.appendDate.IsZero() {
		internalDate = f.appendDate
	}
	msg := syncer.FetchedMessage{Mailbox: mailbox, UID: highest + 1, InternalDate: internalDate, Size: int64(len(raw)), Flags: []string{"\\Seen"}, Raw: raw}
	f.messages[account.UserID] = append(f.messages[account.UserID], msg)
	return msg, nil
}

func (f *fakeFetcher) SetSeen(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, seen bool) error {
	return nil
}

func (f *fakeFetcher) SeenUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	var uids []uint32
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox != mailbox {
			continue
		}
		for _, flag := range msg.Flags {
			if strings.EqualFold(flag, "\\Seen") {
				uids = append(uids, msg.UID)
				break
			}
		}
	}
	return uids, nil
}

func (f *fakeFetcher) SetFlagged(ctx context.Context, account store.MailAccount, mailbox string, uid uint32, flagged bool) error {
	return nil
}

func (f *fakeFetcher) FlaggedUIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	var uids []uint32
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox != mailbox {
			continue
		}
		for _, flag := range msg.Flags {
			if strings.EqualFold(flag, "\\Flagged") {
				uids = append(uids, msg.UID)
				break
			}
		}
	}
	return uids, nil
}

func (f *fakeFetcher) MoveMessage(ctx context.Context, account store.MailAccount, sourceMailbox string, destMailbox string, uid uint32) error {
	return nil
}

func TestFakeSyncTenantIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	blobStore := blob.New(dir)

	user1, err := db.CreateUser(ctx, "one@example.test", "One", "hash-one", false)
	if err != nil {
		t.Fatal(err)
	}
	user2, err := db.CreateUser(ctx, "two@example.test", "Two", "hash-two", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user1.ID, enc)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user2.ID, enc)); err != nil {
		t.Fatal(err)
	}

	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user1.ID: {{
			Mailbox:      "INBOX",
			UID:          1,
			InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Flags:        []string{"\\Seen"},
			Raw:          []byte(rawMessage("u1@example.test", "Tenant one shared needle", "tenant one body shared needle", true)),
		}},
		user2.ID: {{
			Mailbox:      "INBOX",
			UID:          1,
			InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Raw:          []byte(rawMessage("u2@example.test", "Tenant two shared needle", "tenant two body shared needle", false)),
		}},
	}}
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchSvc, Fetcher: fetcher}

	run1, err := service.SyncUser(ctx, user1.ID)
	if err != nil {
		t.Fatal(err)
	}
	run1, err = db.GetSyncRunForUser(ctx, user1.ID, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run1.Status != "ok" || run1.MessagesSeen != 1 || run1.MessagesStored != 1 || run1.MailboxesDone != 1 || run1.MailboxesTotal != 1 {
		t.Fatalf("unexpected sync progress: %+v", run1)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != user1.ID {
		t.Fatalf("fetcher calls = %v", fetcher.calls)
	}
	messages1, err := db.ListMessagesForUser(ctx, user1.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages1) != 1 {
		t.Fatalf("user1 messages = %d", len(messages1))
	}
	if !strings.Contains(messages1[0].BlobPath, fmt.Sprintf("users/%d/blobs/", user1.ID)) {
		t.Fatalf("message blob path is not user scoped: %s", messages1[0].BlobPath)
	}
	attachments, err := db.ListAttachmentsForMessage(ctx, user1.ID, messages1[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("attachments = %d", len(attachments))
	}
	if _, err := db.GetMessageForUser(ctx, user2.ID, messages1[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("user2 guessed user1 message: %v", err)
	}
	if _, err := db.GetAttachmentForUser(ctx, user2.ID, attachments[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("user2 guessed user1 attachment: %v", err)
	}
	if _, err := db.GetBlobForUser(ctx, user2.ID, messages1[0].BlobID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("user2 guessed user1 blob: %v", err)
	}
	if _, err := db.GetSyncRunForUser(ctx, user2.ID, run1.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("user2 guessed user1 sync run: %v", err)
	}
	ids, err := searchSvc.Search(ctx, user2.ID, "tenant one", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("user2 search returned user1 hits: %v", ids)
	}
	ids, err = searchSvc.Search(ctx, user1.ID, "has:attachment filename:note.txt", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != messages1[0].ID {
		t.Fatalf("user1 attachment search = %v", ids)
	}
	ids, err = searchSvc.Search(ctx, user1.ID, "is:read", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != messages1[0].ID {
		t.Fatalf("user1 read search = %v", ids)
	}

	if _, err := service.SyncUser(ctx, user2.ID); err != nil {
		t.Fatal(err)
	}
	user1Hits, err := searchSvc.Search(ctx, user1.ID, "shared needle", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	user2Hits, err := searchSvc.Search(ctx, user2.ID, "shared needle", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(user1Hits) != 1 || user1Hits[0] != messages1[0].ID {
		t.Fatalf("user1 hits = %v", user1Hits)
	}
	if len(user2Hits) != 1 || user2Hits[0] == messages1[0].ID {
		t.Fatalf("user2 hits = %v", user2Hits)
	}
}

func TestRequestedMailboxSyncRepairsIncompleteCheckpoint(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	blobStore := blob.New(dir)

	user, err := db.CreateUser(ctx, "checkpoint@example.test", "Checkpoint", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user.ID, enc)); err != nil {
		t.Fatal(err)
	}

	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {{Mailbox: "INBOX", UID: 3, InternalDate: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("three@example.test", "Three", "body three", false))}},
	}}
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	boxes, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 || boxes[0].LastUID != 3 || boxes[0].LocalMessageCount != 1 {
		t.Fatalf("initial mailbox state = %+v", boxes)
	}

	fetcher.messages[user.ID] = []syncer.FetchedMessage{
		{Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("one@example.test", "One", "body one", false))},
		{Mailbox: "INBOX", UID: 2, InternalDate: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("two@example.test", "Two", "body two", false))},
		{Mailbox: "INBOX", UID: 3, InternalDate: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("three@example.test", "Three", "body three", false))},
	}
	run, err := service.SyncUserMailboxes(ctx, user.ID, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := db.ListMessagesForMailbox(ctx, user.ID, boxes[0].ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages after repair = %d", len(messages))
	}
	if run.MessagesStored != 2 || run.MessagesSkipped != 0 {
		t.Fatalf("repair run progress = %+v", run)
	}
	if len(fetcher.fetchUIDCalls) != 1 || len(fetcher.fetchUIDCalls[0]) != 2 || fetcher.fetchUIDCalls[0][0] != 1 || fetcher.fetchUIDCalls[0][1] != 2 {
		t.Fatalf("repair fetched UIDs = %+v", fetcher.fetchUIDCalls)
	}
}

func TestRepairMailboxSearchIndexIndexesMissingIDsWhenCountsMatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	blobStore := blob.New(dir)

	user, err := db.CreateUser(ctx, "repair@example.test", "Repair", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	accountRec, err := db.UpsertMailAccount(ctx, account(user.ID, enc))
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {{
			Mailbox:      "INBOX",
			UID:          9,
			InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Raw:          []byte(rawMessage("repair@example.test", "Repair document", "needleonly local body", false)),
		}},
	}}
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	boxes, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := db.ListMessagesForMailbox(ctx, user.ID, boxes[0].ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	msg := messages[0]
	if err := searchSvc.DeleteMessage(ctx, user.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	stale := msg
	stale.ID = msg.ID + 999
	stale.Subject = "Stale counted document"
	stale.MessageIDHeader = "<stale-counted@example.test>"
	stale.BodyText = store.MessageBodyPreview("unrelated stale body", store.DefaultMessageBodyPreviewBytes)
	if err := searchSvc.IndexMessage(ctx, stale, nil); err != nil {
		t.Fatal(err)
	}
	count, err := searchSvc.CountMailboxMessages(ctx, user.ID, boxes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("search count = %d", count)
	}
	ids, err := searchSvc.Search(ctx, user.ID, "needleonly", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("needleonly unexpectedly indexed before repair: %v", ids)
	}

	mailbox, err := db.GetMailboxForUser(ctx, user.ID, boxes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateSyncRun(ctx, user.ID, accountRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	progress := store.SyncProgress{}
	indexed, err := service.RepairMailboxSearchIndex(ctx, user.ID, mailbox, run.ID, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("indexed = %d", indexed)
	}
	savedRun, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if savedRun.MessagesTotal != 1 || savedRun.MessagesSeen != 1 || savedRun.MessagesStored != 1 {
		t.Fatalf("repair progress = total %d seen %d stored %d", savedRun.MessagesTotal, savedRun.MessagesSeen, savedRun.MessagesStored)
	}
	if savedRun.CurrentMailbox != mailbox.Name {
		t.Fatalf("current mailbox = %q, want %q", savedRun.CurrentMailbox, mailbox.Name)
	}
	ids, err = searchSvc.Search(ctx, user.ID, "needleonly", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != msg.ID {
		t.Fatalf("needleonly ids = %v, want %d", ids, msg.ID)
	}
}

func TestPurgeMailboxLocalReferencesClearsSearchAndResetsCheckpoint(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	blobStore := blob.New(dir)

	user, err := db.CreateUser(ctx, "purge@example.test", "Purge", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user.ID, enc)); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {{
			Mailbox:      "INBOX",
			UID:          42,
			InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Raw:          []byte(rawMessage("purge@example.test", "Purgeable", "purgeable local body", false)),
		}},
	}}
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	boxes, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := db.ListMessagesForMailbox(ctx, user.ID, boxes[0].ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	msg := messages[0]
	boxBefore, err := db.GetMailboxForUser(ctx, user.ID, boxes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if boxBefore.LastUID == 0 {
		t.Fatalf("last uid was not advanced before purge: %+v", boxBefore)
	}

	purged, err := service.PurgeMailboxLocalReferences(ctx, user.ID, boxes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d", purged)
	}
	messages, err = db.ListMessagesForMailbox(ctx, user.ID, boxes[0].ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages after purge = %d", len(messages))
	}
	count, err := searchSvc.CountMailboxMessages(ctx, user.ID, boxes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("search docs after purge = %d", count)
	}
	boxAfter, err := db.GetMailboxForUser(ctx, user.ID, boxes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if boxAfter.LastUID != 0 {
		t.Fatalf("last uid after purge = %d", boxAfter.LastUID)
	}
	summaries, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d", len(summaries))
	}
	if summaries[0].LocalMessageCount != 0 || summaries[0].LocalSyncPercent != 0 {
		t.Fatalf("local summary after purge = count %d percent %d", summaries[0].LocalMessageCount, summaries[0].LocalSyncPercent)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := db.GetBlobForUser(ctx, user.ID, msg.BlobID)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("blob row still present after async cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSyncModesControlAutomaticAndManualFolders(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	blobStore := blob.New(dir)
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "modes@example.test", "Modes", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	accountRec, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:              user.ID,
		Email:               "modes@example.test",
		Host:                "imap.example.test",
		Port:                993,
		Username:            "modes",
		EncryptedPassword:   enc,
		UseTLS:              true,
		Mailbox:             "INBOX,Archive,Spam",
		SyncIntervalMinutes: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := db.GetOrCreateMailbox(ctx, user.ID, accountRec.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := db.GetOrCreateMailbox(ctx, user.ID, accountRec.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	spam, err := db.GetOrCreateMailbox(ctx, user.ID, accountRec.ID, "Spam")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSyncMode(ctx, user.ID, archive.ID, "manual"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSyncMode(ctx, user.ID, spam.ID, "never"); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {
			{Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("inbox@example.test", "Inbox", "inbox body", false))},
			{Mailbox: "Archive", UID: 1, InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("archive@example.test", "Archive", "archive body", false))},
			{Mailbox: "Spam", UID: 1, InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("spam@example.test", "Spam", "spam body", false))},
		},
	}}
	service := &syncer.Service{Store: db, Blobs: blobStore, Search: searchSvc, Fetcher: fetcher}
	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	inboxMsgs, err := db.ListMessagesForMailbox(ctx, user.ID, inbox.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	archiveMsgs, err := db.ListMessagesForMailbox(ctx, user.ID, archive.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	spamMsgs, err := db.ListMessagesForMailbox(ctx, user.ID, spam.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(inboxMsgs) != 1 || len(archiveMsgs) != 0 || len(spamMsgs) != 0 {
		t.Fatalf("auto sync counts inbox=%d archive=%d spam=%d", len(inboxMsgs), len(archiveMsgs), len(spamMsgs))
	}
	if _, err := service.SyncUserMailboxes(ctx, user.ID, []string{"Archive", "Spam"}); err != nil {
		t.Fatal(err)
	}
	archiveMsgs, err = db.ListMessagesForMailbox(ctx, user.ID, archive.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	spamMsgs, err = db.ListMessagesForMailbox(ctx, user.ID, spam.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(archiveMsgs) != 1 || len(spamMsgs) != 0 {
		t.Fatalf("manual sync counts archive=%d spam=%d", len(archiveMsgs), len(spamMsgs))
	}
}

func TestDiscoverMailboxesCreatesRowsWithoutSyncingMessages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "discover@example.test", "Discover", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:              user.ID,
		Email:               "discover@example.test",
		Host:                "imap.example.test",
		Port:                993,
		Username:            "discover",
		EncryptedPassword:   enc,
		UseTLS:              true,
		Mailbox:             "*",
		SyncIntervalMinutes: 15,
	}); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{user.ID: {
		{Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("inbox@example.test", "Inbox", "inbox body", false))},
	}}}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}
	count, err := service.DiscoverMailboxes(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("discovered = %d", count)
	}
	boxes, err := db.ListMailboxesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 1 || boxes[0].Name != "INBOX" {
		t.Fatalf("boxes = %+v", boxes)
	}
	messages, err := db.ListMessagesForUser(ctx, user.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("discovery fetched messages: %+v", messages)
	}
}

func TestCanceledSyncRunIsMarkedInterrupted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "cancel@example.test", "Cancel", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertMailAccount(ctx, account(user.ID, enc)); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	fetcher := &cancelingFetcher{
		fakeFetcher: &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
			user.ID: {
				{Mailbox: "INBOX", UID: 1, InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Raw: []byte(rawMessage("cancel@example.test", "Cancel", "body", false))},
			},
		}},
		cancel: cancel,
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher}

	run, err := service.SyncUser(runCtx, user.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sync error = %v, want context.Canceled", err)
	}
	saved, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "interrupted" {
		t.Fatalf("status = %q, run = %+v", saved.Status, saved)
	}
	if saved.Error == "" {
		t.Fatalf("expected interruption error text")
	}
	if saved.FinishedAt.IsZero() {
		t.Fatalf("finished_at was not set: %+v", saved)
	}
}

func TestOnDemandFetchCachesRawBlobButPlainFetchDoesNot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := []byte("12345678901234567890123456789012")
	enc, err := mmcrypto.EncryptString(key, "unused")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "cache@example.test", "Cache", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, account(user.ID, enc))
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	remoteBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message-remote", Path: "remote/cache.eml", SHA256: "remote", Size: 0})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:       user.ID,
		AccountID:    account.ID,
		MailboxID:    mailbox.ID,
		BlobID:       remoteBlob.ID,
		Subject:      "Cache me",
		FromAddr:     "sender@example.test",
		Date:         time.Date(2014, 10, 2, 12, 0, 0, 0, time.UTC),
		InternalDate: time.Date(2014, 10, 2, 12, 0, 0, 0, time.UTC),
		UID:          77,
		BodyText:     store.MessageBodyPreview("cached body", store.DefaultMessageBodyPreviewBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {{Mailbox: "INBOX", UID: 77, InternalDate: msg.InternalDate, Raw: []byte(rawMessage("sender@example.test", "Cache me", "cached body", true))}},
	}}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher}

	if _, err := service.FetchRawMessageForMessage(ctx, user.ID, msg); err != nil {
		t.Fatal(err)
	}
	plainFetched, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plainFetched.BlobPath != "" {
		t.Fatalf("plain fetch cached blob path %q", plainFetched.BlobPath)
	}

	if _, err := service.FetchAndCacheRawMessageForMessage(ctx, user.ID, plainFetched); err != nil {
		t.Fatal(err)
	}
	cached, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cached.BlobPath == "" || !strings.Contains(cached.BlobPath, fmt.Sprintf("users/%d/blobs/", user.ID)) {
		t.Fatalf("cached blob path = %q", cached.BlobPath)
	}
	blobRec, err := db.GetBlobForUser(ctx, user.ID, cached.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if blobRec.Kind != "message-cache" || blobRec.Size == 0 {
		t.Fatalf("cached blob = %+v", blobRec)
	}
}

func TestCopyMessageAcrossAccountsAppendsAndStoresDestination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	user, err := db.CreateUser(ctx, "copy@example.test", "Copy", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	sourceAccount, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "source@example.test", Host: "imap.source.test", Port: 993, Username: "source", EncryptedPassword: "secret", UseTLS: true, Mailbox: store.DefaultMailboxPattern})
	if err != nil {
		t.Fatal(err)
	}
	destAccount, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "demo@example.test", Host: "imap.demo.test", Port: 993, Username: "demo", EncryptedPassword: "secret", UseTLS: true, Mailbox: store.DefaultMailboxPattern})
	if err != nil {
		t.Fatal(err)
	}
	sourceMailbox, err := db.GetOrCreateMailbox(ctx, user.ID, sourceAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	destMailbox, err := db.GetOrCreateMailbox(ctx, user.ID, destAccount.ID, "Demo Spam")
	if err != nil {
		t.Fatal(err)
	}
	remoteBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message-remote", Path: "remote/source.eml", SHA256: "remote", Size: 0})
	if err != nil {
		t.Fatal(err)
	}
	sourceRaw := []byte(rawMessage("spam@example.test", "Demo spam copy", "spam body", false))
	sourceMessage, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:          user.ID,
		AccountID:       sourceAccount.ID,
		MailboxID:       sourceMailbox.ID,
		BlobID:          remoteBlob.ID,
		MessageIDHeader: "<demo-spam-copy@example.test>",
		Subject:         "Demo spam copy",
		FromAddr:        "spam@example.test",
		Date:            time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		InternalDate:    time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		UID:             42,
		BodyText:        store.MessageBodyPreview("spam body", store.DefaultMessageBodyPreviewBytes),
		IsRead:          false,
		IsStarred:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{messages: map[int64][]syncer.FetchedMessage{
		user.ID: {{Mailbox: "INBOX", UID: 42, InternalDate: sourceMessage.InternalDate, Raw: sourceRaw}},
	}}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}

	copied, err := service.CopyMessages(ctx, user.ID, []int64{sourceMessage.ID}, destMailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copied != 1 {
		t.Fatalf("copied = %d, want 1", copied)
	}
	if _, err := db.GetMessageForUser(ctx, user.ID, sourceMessage.ID); err != nil {
		t.Fatalf("source message missing after copy: %v", err)
	}
	destMessages, err := db.ListMessagesForMailbox(ctx, user.ID, destMailbox.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(destMessages) != 1 {
		t.Fatalf("destination messages = %d, want 1", len(destMessages))
	}
	copiedMessage := destMessages[0]
	if copiedMessage.AccountID != destAccount.ID || copiedMessage.MailboxID != destMailbox.ID || copiedMessage.Subject != "Demo spam copy" {
		t.Fatalf("copied message = %+v, want dest account/mailbox with original subject", copiedMessage)
	}
	if copiedMessage.IsRead || !copiedMessage.IsStarred {
		t.Fatalf("copied flags read=%v starred=%v, want unread/starred", copiedMessage.IsRead, copiedMessage.IsStarred)
	}
}

func TestCopyMessagesPreservesSourceDateWhenDestinationUsesAppendDate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchSvc, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchSvc.Close()
	user, err := db.CreateUser(ctx, "copy-date@example.test", "Copy Date", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	sourceAccount, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "source-date@example.test", Host: "imap.source.test", Port: 993, Username: "source-date", EncryptedPassword: "secret", UseTLS: true, Mailbox: store.DefaultMailboxPattern})
	if err != nil {
		t.Fatal(err)
	}
	destAccount, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: "dest-date@example.test", Host: "imap.dest.test", Port: 993, Username: "dest-date", EncryptedPassword: "secret", UseTLS: true, Mailbox: store.DefaultMailboxPattern})
	if err != nil {
		t.Fatal(err)
	}
	sourceMailbox, err := db.GetOrCreateMailbox(ctx, user.ID, sourceAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	destMailbox, err := db.GetOrCreateMailbox(ctx, user.ID, destAccount.ID, "Copied")
	if err != nil {
		t.Fatal(err)
	}
	oldDate := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	newDate := time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	appendDate := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC)
	oldRaw := []byte(rawMessageNoDate("old@example.test", "Old no date", "old body"))
	newRaw := []byte(rawMessageNoDate("new@example.test", "New no date", "new body"))
	oldBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message-remote", Path: "remote/old-no-date.eml", SHA256: "old", Size: 0})
	if err != nil {
		t.Fatal(err)
	}
	newBlob, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message-remote", Path: "remote/new-no-date.eml", SHA256: "new", Size: 0})
	if err != nil {
		t.Fatal(err)
	}
	oldMessage, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:       user.ID,
		AccountID:    sourceAccount.ID,
		MailboxID:    sourceMailbox.ID,
		BlobID:       oldBlob.ID,
		Subject:      "Old no date",
		FromAddr:     "old@example.test",
		Date:         oldDate,
		InternalDate: oldDate,
		UID:          10,
		BodyText:     store.MessageBodyPreview("old body", store.DefaultMessageBodyPreviewBytes),
		IsRead:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	newMessage, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:       user.ID,
		AccountID:    sourceAccount.ID,
		MailboxID:    sourceMailbox.ID,
		BlobID:       newBlob.ID,
		Subject:      "New no date",
		FromAddr:     "new@example.test",
		Date:         newDate,
		InternalDate: newDate,
		UID:          11,
		BodyText:     store.MessageBodyPreview("new body", store.DefaultMessageBodyPreviewBytes),
		IsRead:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{
			user.ID: {
				{Mailbox: "INBOX", UID: oldMessage.UID, InternalDate: oldMessage.InternalDate, Raw: oldRaw},
				{Mailbox: "INBOX", UID: newMessage.UID, InternalDate: newMessage.InternalDate, Raw: newRaw},
			},
		},
		appendDate: appendDate,
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchSvc, Fetcher: fetcher}

	if _, err := service.CopyMessages(ctx, user.ID, []int64{newMessage.ID, oldMessage.ID}, destMailbox.ID); err != nil {
		t.Fatal(err)
	}
	destMessages, err := db.ListMessagesForMailbox(ctx, user.ID, destMailbox.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(destMessages) != 2 {
		t.Fatalf("destination messages = %d, want 2", len(destMessages))
	}
	if destMessages[0].Subject != "New no date" || !destMessages[0].Date.Equal(newDate) {
		t.Fatalf("first copied message = %q %s, want New no date at %s", destMessages[0].Subject, destMessages[0].Date, newDate)
	}
	if destMessages[1].Subject != "Old no date" || !destMessages[1].Date.Equal(oldDate) {
		t.Fatalf("second copied message = %q %s, want Old no date at %s", destMessages[1].Subject, destMessages[1].Date, oldDate)
	}
}

func account(userID int64, encryptedPassword string) store.MailAccount {
	return store.MailAccount{
		UserID:              userID,
		Email:               fmt.Sprintf("user%d@example.test", userID),
		Host:                "imap.example.test",
		Port:                993,
		Username:            fmt.Sprintf("user%d", userID),
		EncryptedPassword:   encryptedPassword,
		UseTLS:              true,
		Mailbox:             "INBOX",
		SyncIntervalMinutes: 15,
	}
}

func rawMessage(from, subject, body string, withAttachment bool) string {
	if !withAttachment {
		return fmt.Sprintf("From: %s\r\nTo: archive@example.test\r\nSubject: %s\r\nDate: Fri, 01 May 2026 12:00:00 +0000\r\nMessage-ID: <%s@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", from, subject, strings.ReplaceAll(subject, " ", "-"), body)
	}
	return fmt.Sprintf("From: %s\r\nTo: archive@example.test\r\nSubject: %s\r\nDate: Fri, 01 May 2026 12:00:00 +0000\r\nMessage-ID: <%s@example.test>\r\nContent-Type: multipart/mixed; boundary=rolltop-test\r\n\r\n--rolltop-test\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n--rolltop-test\r\nContent-Type: text/plain; name=\"note.txt\"\r\nContent-Disposition: attachment; filename=\"note.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\nbm90ZSBib2R5\r\n--rolltop-test--\r\n", from, subject, strings.ReplaceAll(subject, " ", "-"), body)
}

func rawMessageNoDate(from, subject, body string) string {
	return fmt.Sprintf("From: %s\r\nTo: archive@example.test\r\nSubject: %s\r\nMessage-ID: <%s@example.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", from, subject, strings.ReplaceAll(subject, " ", "-"), body)
}
