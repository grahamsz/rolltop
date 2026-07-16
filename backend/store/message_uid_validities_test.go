package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMessageUIDValiditiesForUserIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, account, mailbox, blob := testMailbox(t, ctx, db)
	const ownerGeneration uint32 = 77
	if err := db.UpdateMailboxRemoteStatus(ctx, owner.ID, mailbox.ID, 1, 0, 2, ownerGeneration); err != nil {
		t.Fatal(err)
	}
	ownerMessage, err := db.CreateMessage(ctx, CreateMessage{
		UserID: owner.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<uid-validity-owner@example.test>", Date: time.Now().UTC(),
		InternalDate: time.Now().UTC(), UID: 1, UIDValidity: int64(ownerGeneration),
	})
	if err != nil {
		t.Fatal(err)
	}

	other, err := db.CreateUser(ctx, "uid-validity-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.UpsertMailAccount(ctx, MailAccount{
		UserID: other.ID, Email: "uid-validity-other@example.test", Host: "imap.example.test", Port: 993,
		Username: "other", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	const otherGeneration uint32 = 88
	if err := db.UpdateMailboxRemoteStatus(ctx, other.ID, otherMailbox.ID, 1, 0, 2, otherGeneration); err != nil {
		t.Fatal(err)
	}
	otherBlob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: other.ID, Kind: "message", Path: "users/2/blobs/accounts/1/mailboxes/INBOX/uid-1.eml",
		SHA256: "other", Size: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherMessage, err := db.CreateMessage(ctx, CreateMessage{
		UserID: other.ID, AccountID: otherAccount.ID, MailboxID: otherMailbox.ID, BlobID: otherBlob.ID,
		MessageIDHeader: "<uid-validity-other@example.test>", Date: time.Now().UTC(),
		InternalDate: time.Now().UTC(), UID: 1, UIDValidity: int64(otherGeneration),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.MessageUIDValiditiesForUser(ctx, owner.ID, []int64{
		ownerMessage.ID, otherMessage.ID, ownerMessage.ID, 0, -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[ownerMessage.ID] != int64(ownerGeneration) {
		t.Fatalf("owner generations=%v, want only message %d generation %d", got, ownerMessage.ID, ownerGeneration)
	}
	if _, ok := got[otherMessage.ID]; ok {
		t.Fatalf("cross-tenant message %d leaked into owner result %v", otherMessage.ID, got)
	}
	if _, err := db.MessageUIDValiditiesForUser(ctx, 0, []int64{ownerMessage.ID}); err == nil {
		t.Fatal("invalid user ID was accepted")
	}
}
