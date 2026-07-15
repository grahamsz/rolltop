package syncer_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	mmcrypto "rolltop/backend/crypto"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

func TestSyncResetsMailboxGenerationAndRefetchesReusedUID(t *testing.T) {
	for _, tc := range []struct {
		name            string
		prepareCachedDB func(t *testing.T, db *store.Store, userID int64, mailbox store.Mailbox, message store.MessageRecord)
	}{
		{name: "remote UIDVALIDITY changed"},
		{
			name: "upgrade row is unproven under current cached UIDVALIDITY",
			prepareCachedDB: func(t *testing.T, db *store.Store, userID int64, mailbox store.Mailbox, message store.MessageRecord) {
				t.Helper()
				// Model an upgrade after an empty STATUS snapshot. Without an explicit
				// backfill guard, the reused UID fetched below looks like new mail.
				if err := db.UpdateMailboxRemoteStatus(context.Background(), userID, mailbox.ID, 0, 0, 1, 2); err != nil {
					t.Fatal(err)
				}
				if _, err := db.DB().Exec(`UPDATE messages SET uid_validity = 0 WHERE user_id = ? AND id = ?`, userID, message.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			user, err := db.CreateUser(ctx, "generation-reset@example.test", "Generation Reset", "hash", false)
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

			oldRaw := []byte(rawMessage("old-generation@example.test", "Old generation", "oldgenerationtoken", false))
			newRaw := []byte(rawMessage("new-generation@example.test", "New generation", "newgenerationtoken", false))
			fetcher := &fakeFetcher{
				messages:             map[int64][]syncer.FetchedMessage{user.ID: {{Mailbox: "INBOX", UID: 5, InternalDate: time.Now().UTC(), Raw: oldRaw}}},
				mailboxes:            []syncer.MailboxInfo{{Name: "INBOX"}},
				uidValidityByMailbox: map[string]uint32{"inbox": 1},
			}
			service := &syncer.Service{Store: db, Blobs: blob.New(dir), Search: searchService, Fetcher: fetcher}
			if _, err := service.SyncUser(ctx, user.ID); err != nil {
				t.Fatal(err)
			}
			mailbox, err := db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			messages, err := db.ListMessagesForMailbox(ctx, user.ID, mailbox.ID, 10, 0)
			if err != nil || len(messages) != 1 {
				t.Fatalf("initial messages=%+v err=%v", messages, err)
			}
			oldMessage := messages[0]
			if tc.prepareCachedDB != nil {
				tc.prepareCachedDB(t, db, user.ID, mailbox, oldMessage)
			}

			fetcher.messages[user.ID] = []syncer.FetchedMessage{{Mailbox: "INBOX", UID: 5, InternalDate: time.Now().UTC().Add(time.Minute), Raw: newRaw}}
			fetcher.uidValidityByMailbox["inbox"] = 2
			fetcher.fetchAfterUIDs = nil
			if _, err := service.SyncUser(ctx, user.ID); err != nil {
				t.Fatal(err)
			}
			if len(fetcher.fetchAfterUIDs) == 0 || fetcher.fetchAfterUIDs[0] != 0 {
				t.Fatalf("post-reset fetch checkpoints=%v, want first fetch after UID 0", fetcher.fetchAfterUIDs)
			}
			mailbox, err = db.GetMailbox(ctx, user.ID, accountRecord.ID, "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			if mailbox.UIDValidity != 2 || mailbox.LastUID != 5 {
				t.Fatalf("mailbox after reset UIDVALIDITY=%d lastUID=%d, want 2/5", mailbox.UIDValidity, mailbox.LastUID)
			}
			messages, err = db.ListMessagesForMailbox(ctx, user.ID, mailbox.ID, 10, 0)
			if err != nil || len(messages) != 1 || messages[0].ID == oldMessage.ID || messages[0].Subject != "New generation" {
				t.Fatalf("refetched messages=%+v err=%v", messages, err)
			}
			var storedUIDValidity int64
			if err := db.DB().QueryRow(`SELECT uid_validity FROM messages WHERE user_id = ? AND id = ?`, user.ID, messages[0].ID).Scan(&storedUIDValidity); err != nil {
				t.Fatal(err)
			}
			if storedUIDValidity != 2 {
				t.Fatalf("refetched message UIDVALIDITY=%d, want 2", storedUIDValidity)
			}
			var oldBlobRows, tombstones int
			if err := db.DB().QueryRow(`SELECT COUNT(*) FROM blobs WHERE user_id = ? AND id = ?`, user.ID, oldMessage.BlobID).Scan(&oldBlobRows); err != nil {
				t.Fatal(err)
			}
			if err := db.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints WHERE user_id = ? AND source_mailbox_id = ?`, user.ID, mailbox.ID).Scan(&tombstones); err != nil {
				t.Fatal(err)
			}
			if oldBlobRows != 0 || tombstones != 0 {
				t.Fatalf("reset retained old blobs=%d or created tombstones=%d", oldBlobRows, tombstones)
			}
			events, eventCount, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if eventCount != 0 || len(events) != 0 {
				t.Fatalf("generation backfill created new-mail events=%+v count=%d", events, eventCount)
			}
			oldHits, err := searchService.Search(ctx, user.ID, "oldgenerationtoken", 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			newHits, err := searchService.Search(ctx, user.ID, "newgenerationtoken", 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(oldHits) != 1 || len(newHits) != 1 || newHits[0] != messages[0].ID {
				t.Fatalf("search during bounded reset old=%v new=%v want deferred stale hit/[new message]", oldHits, newHits)
			}
			if _, err := service.SyncUser(ctx, user.ID); err != nil {
				t.Fatal(err)
			}
			oldHits, err = searchService.Search(ctx, user.ID, "oldgenerationtoken", 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(oldHits) != 0 {
				t.Fatalf("post-recovery search repair retained stale hits=%v", oldHits)
			}
		})
	}
}
