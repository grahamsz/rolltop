package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mailmirror/internal/blob"
	mmcrypto "mailmirror/internal/crypto"
	"mailmirror/internal/search"
	"mailmirror/internal/store"
	"mailmirror/internal/syncer"
)

type fakeFetcher struct {
	messages map[int64][]syncer.FetchedMessage
	calls    []int64
}

type cancelingFetcher struct {
	*fakeFetcher
	cancel context.CancelFunc
}

func (f *cancelingFetcher) FetchMailbox(ctx context.Context, account store.MailAccount, mailbox string, afterUID uint32, handle func(syncer.FetchedMessage) error) error {
	f.cancel()
	return ctx.Err()
}

func (f *fakeFetcher) ListMailboxes(ctx context.Context, account store.MailAccount) ([]syncer.MailboxInfo, error) {
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

func (f *fakeFetcher) FetchMessage(ctx context.Context, account store.MailAccount, mailbox string, uid uint32) (syncer.FetchedMessage, error) {
	for _, msg := range f.messages[account.UserID] {
		if msg.Mailbox == mailbox && msg.UID == uid {
			return msg, nil
		}
	}
	return syncer.FetchedMessage{}, store.ErrNotFound
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
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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
	if !strings.Contains(messages1[0].BlobPath, fmt.Sprintf("blobs/users/%d/", user1.ID)) {
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
	ids, err := searchSvc.Search(ctx, user2.ID, "tenant one", search.SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("user2 search returned user1 hits: %v", ids)
	}
	ids, err = searchSvc.Search(ctx, user1.ID, "has:attachment filename:note.txt", search.SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != messages1[0].ID {
		t.Fatalf("user1 attachment search = %v", ids)
	}
	ids, err = searchSvc.Search(ctx, user1.ID, "is:read", search.SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != messages1[0].ID {
		t.Fatalf("user1 read search = %v", ids)
	}

	if _, err := service.SyncUser(ctx, user2.ID); err != nil {
		t.Fatal(err)
	}
	user1Hits, err := searchSvc.Search(ctx, user1.ID, "shared needle", search.SortBest, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	user2Hits, err := searchSvc.Search(ctx, user2.ID, "shared needle", search.SortBest, 10, 0)
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

func TestRebuildMailboxSearchIndexRefreshesLanguage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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

	user, err := db.CreateUser(ctx, "one@example.test", "One", "hash-one", false)
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
			UID:          1,
			InternalDate: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Raw: []byte(rawMessage(
				"fr@example.test",
				"Bonjour",
				"Ceci est un message en français avec assez de contexte pour identifier correctement la langue.",
				false,
			)),
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
	if len(boxes) != 1 {
		t.Fatalf("mailboxes = %d", len(boxes))
	}
	messages, err := db.ListMessagesForMailbox(ctx, user.ID, boxes[0].ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d", len(messages))
	}
	msg := messages[0]
	msg.LanguageCode = "en"
	if err := db.UpdateMessageLanguage(ctx, user.ID, msg.ID, "en"); err != nil {
		t.Fatal(err)
	}
	if err := searchSvc.IndexMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	run, err := service.StartRebuildMailboxSearchIndex(ctx, user.ID, boxes[0].ID, func() {
		close(done)
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for rebuild run %d", run.ID)
	}

	refreshed, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.LanguageCode != "fr" {
		t.Fatalf("language = %q", refreshed.LanguageCode)
	}
	ids, err := searchSvc.Search(ctx, user.ID, "lang:fr", search.SortRecent, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != msg.ID {
		t.Fatalf("lang:fr ids = %v", ids)
	}
}

func TestSyncModesControlAutomaticAndManualFolders(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
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
	return fmt.Sprintf("From: %s\r\nTo: archive@example.test\r\nSubject: %s\r\nDate: Fri, 01 May 2026 12:00:00 +0000\r\nMessage-ID: <%s@example.test>\r\nContent-Type: multipart/mixed; boundary=mailmirror-test\r\n\r\n--mailmirror-test\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n--mailmirror-test\r\nContent-Type: text/plain; name=\"note.txt\"\r\nContent-Disposition: attachment; filename=\"note.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\nbm90ZSBib2R5\r\n--mailmirror-test--\r\n", from, subject, strings.ReplaceAll(subject, " ", "-"), body)
}
