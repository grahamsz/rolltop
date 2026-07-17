package syncer

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestPurgeMailboxSearchIndexReportsIncrementalProgressAt251Documents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	searchService, err := search.OpenPerUser(filepath.Join(dir, "users"))
	if err != nil {
		t.Fatal(err)
	}
	defer searchService.Close()

	user, err := db.CreateUser(ctx, "purge-progress@example.test", "Purge progress", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Gmail Forward")
	if err != nil {
		t.Fatal(err)
	}

	const documents = 251
	items := make([]search.MessageIndexDocument, 0, documents)
	for id := int64(1); id <= documents; id++ {
		items = append(items, search.MessageIndexDocument{Message: store.MessageRecord{
			ID: id, UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID,
			Subject: "purge progress", Date: time.Unix(id, 0).UTC(),
		}})
	}
	if err := searchService.IndexMessages(ctx, items); err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}

	type snapshot struct {
		seen    int
		total   int
		subject string
	}
	var snapshots []snapshot
	service := &Service{Store: db, Search: searchService}
	service.NotifyProgress = func(notifiedUserID int64) {
		if notifiedUserID != user.ID {
			t.Fatalf("progress notified user=%d, want %d", notifiedUserID, user.ID)
		}
		stored, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot{
			seen: stored.MessagesSeen, total: stored.MessagesTotal, subject: stored.LatestNewSubject,
		})
	}
	progress := store.SyncProgress{MailboxesTotal: 1}
	deleted, err := service.PurgeMailboxSearchIndexWithProgress(ctx, user.ID, mailbox.ID, run.ID, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != documents {
		t.Fatalf("deleted=%d, want %d", deleted, documents)
	}
	want := []snapshot{
		{seen: 0, total: documents, subject: "Purging full-text index"},
		{seen: 100, total: documents, subject: "Purging full-text index"},
		{seen: 200, total: documents, subject: "Purging full-text index"},
		{seen: documents, total: documents, subject: "Purging full-text index"},
	}
	if !reflect.DeepEqual(snapshots, want) {
		t.Fatalf("progress snapshots=%+v, want %+v", snapshots, want)
	}
}
