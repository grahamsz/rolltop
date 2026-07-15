package syncer_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type resetDuringMailboxStatusFetcher struct {
	*fakeFetcher
	beforeStatus func()
}

func (f *resetDuringMailboxStatusFetcher) MailboxStatus(ctx context.Context, account store.MailAccount, mailbox string) (syncer.MailboxStatus, error) {
	if f.beforeStatus != nil {
		hook := f.beforeStatus
		f.beforeStatus = nil
		hook()
	}
	return f.fakeFetcher.MailboxStatus(ctx, account, mailbox)
}

func TestPassiveStatusCannotReplaceActiveRebuildGeneration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "passive-rebuild@example.test", "Passive Rebuild", "hash", false)
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
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 7, 4, 8, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxLastUID(ctx, user.ID, mailbox.ID, 1); err != nil {
		t.Fatal(err)
	}
	if stale, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, accountRecord.ID, mailbox.ID, 2); err != nil || !reset || len(stale) != 0 {
		t.Fatalf("empty reset stale=%v reset=%t err=%v", stale, reset, err)
	}
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || !pending {
		t.Fatalf("target-2 rebuild pending=%t err=%v", pending, err)
	}

	now := time.Date(2026, time.July, 14, 21, 30, 0, 0, time.UTC)
	remoteRaw := []byte(rawMessage("passive-generation@example.test", "Generation three", "generation-three", false))
	remote := &fakeFetcher{
		messages: map[int64][]syncer.FetchedMessage{
			user.ID: {{Mailbox: "INBOX", UID: 1, InternalDate: now, Raw: remoteRaw}},
		},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 3},
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: remote}
	if _, err := service.DiscoverMailboxes(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 2 || mailbox.RemoteMessageCount != 7 || mailbox.RemoteUnreadCount != 4 || mailbox.RemoteUIDNext != 8 {
		t.Fatalf("passive target-3 STATUS mutated target-2 mailbox=%+v", mailbox)
	}
	pending, err = db.MailboxGenerationRebuildPending(ctx, user.ID, accountRecord.ID, mailbox.ID, 2)
	if err != nil || !pending {
		t.Fatalf("passive STATUS replaced target-2 marker pending=%t err=%v", pending, err)
	}

	if _, err := service.SyncUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 3 || mailbox.LastUID != 1 {
		t.Fatalf("active target-3 sync mailbox=%+v", mailbox)
	}
	marker, err := db.MailboxGenerationRebuildExists(ctx, user.ID, accountRecord.ID, mailbox.ID)
	if err != nil || marker {
		t.Fatalf("completed target-3 marker exists=%t err=%v", marker, err)
	}
	events, count, cursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || cursor != 0 || len(events) != 0 {
		t.Fatalf("generation rebuild emitted notifications events=%+v count=%d cursor=%d", events, count, cursor)
	}
}

func TestPassiveStatusCannotRegressConcurrentGenerationReset(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "passive-race@example.test", "Passive Race", "hash", false)
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
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	base := &fakeFetcher{
		messages:             map[int64][]syncer.FetchedMessage{user.ID: {}},
		mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
		uidValidityByMailbox: map[string]uint32{"inbox": 1},
	}
	fetcher := &resetDuringMailboxStatusFetcher{fakeFetcher: base}
	fetcher.beforeStatus = func() {
		if _, reset, resetErr := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, accountRecord.ID, mailbox.ID, 2); resetErr != nil || reset {
			t.Fatalf("concurrent initialization reset=%t err=%v", reset, resetErr)
		}
	}
	service := &syncer.Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher}
	if _, err := service.DiscoverMailboxes(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 2 || mailbox.RemoteMessageCount != 0 || mailbox.RemoteUIDNext != 0 {
		t.Fatalf("stale initialization regressed concurrent generation=%+v", mailbox)
	}

	base.uidValidityByMailbox["inbox"] = 2
	fetcher.beforeStatus = func() {
		if _, reset, resetErr := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, accountRecord.ID, mailbox.ID, 3); resetErr != nil || !reset {
			t.Fatalf("concurrent established reset=%t err=%v", reset, resetErr)
		}
	}
	if _, err := service.DiscoverMailboxes(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 3 || mailbox.RemoteMessageCount != 0 || mailbox.RemoteUIDNext != 0 {
		t.Fatalf("stale matching STATUS regressed concurrent reset=%+v", mailbox)
	}
}
