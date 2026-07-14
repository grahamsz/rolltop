// File overview: Search batch consistency when messages move during indexing.

package syncer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestFetchedSearchBatchRemovesMessageDeletedBeforeMetadataCommit(t *testing.T) {
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

	user, err := db.CreateUser(ctx, "batch-move@example.test", "Batch Move", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "batch-move", EncryptedPassword: "encrypted", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Spam")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: user.ID, Kind: "message-remote", Path: "remote/batch-move.eml", SHA256: "batch-move", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		Subject: "Moving while indexing", FromAddr: "sender@example.test", UID: 10,
		Date: time.Now().UTC(), InternalDate: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	service := &Service{Store: db, Search: searchService}
	batch := newFetchedSearchIndexBatch(service)
	if err := batch.Add(ctx, &pendingFetchedSearchIndex{
		Document: search.MessageIndexDocument{Message: message},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMessageForUser(ctx, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	if err := batch.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	indexed, err := searchService.MessageIDsIndexed(ctx, user.ID, []int64{message.ID})
	if err != nil {
		t.Fatal(err)
	}
	if indexed[message.ID] {
		t.Fatal("deleted message was resurrected in the search index")
	}
}
