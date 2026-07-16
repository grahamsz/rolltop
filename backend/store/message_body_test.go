package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestRetainMessageBlobIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, account, mailbox, remoteBlob := testMailbox(t, ctx, db)
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID:       owner.ID,
		AccountID:    account.ID,
		MailboxID:    mailbox.ID,
		BlobID:       remoteBlob.ID,
		Subject:      "Retain me",
		Date:         time.Now().UTC(),
		InternalDate: time.Now().UTC(),
		UID:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-retained@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	foreignPath := fmt.Sprintf("users/%d/blobs/foreign.eml", other.ID)
	if _, err := db.RetainMessageBlob(ctx, other.ID, message.ID, BlobRecord{
		Path: foreignPath, SHA256: "foreign", Size: 7,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant retain error=%v, want not found", err)
	}
	if _, err := db.GetBlobByPathForUser(ctx, other.ID, foreignPath); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant retain created blob metadata: %v", err)
	}

	ownerPath := fmt.Sprintf("users/%d/blobs/retained.eml", owner.ID)
	retained, err := db.RetainMessageBlob(ctx, owner.ID, message.ID, BlobRecord{
		Path: ownerPath, SHA256: "retained", Size: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retained.UserID != owner.ID || retained.BlobPath != ownerPath {
		t.Fatalf("retained message=%+v", retained)
	}
	if retained.BlobID != remoteBlob.ID {
		t.Fatalf("retained blob ID=%d, want in-place replacement %d", retained.BlobID, remoteBlob.ID)
	}
	metadata, err := db.GetBlobForUser(ctx, owner.ID, retained.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Kind != "message" || metadata.Size != 8 {
		t.Fatalf("retained metadata=%+v", metadata)
	}
	ownerMessage, err := db.GetMessageForUser(ctx, owner.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ownerMessage.BlobPath != ownerPath {
		t.Fatalf("owner blob path=%q, want %q", ownerMessage.BlobPath, ownerPath)
	}
	if _, err := db.GetBlobByPathForUser(ctx, owner.ID, remoteBlob.Path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("former remote blob metadata survived replacement: %v", err)
	}

	secondRemote, err := db.CreateBlob(ctx, BlobRecord{
		UserID: owner.ID, Kind: "message-remote", Path: fmt.Sprintf("remote/users/%d/uid-2.eml", owner.ID), SHA256: "remote-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateMessage(ctx, CreateMessage{
		UserID: owner.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: secondRemote.ID,
		Subject: "Retain existing target", Date: time.Now().UTC(), InternalDate: time.Now().UTC(), UID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	existingPath := fmt.Sprintf("users/%d/blobs/existing-retained.eml", owner.ID)
	existingTarget, err := db.CreateBlob(ctx, BlobRecord{
		UserID: owner.ID, Kind: "message", Path: existingPath, SHA256: "old-attempt", Size: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err = db.RetainMessageBlob(ctx, owner.ID, second.ID, BlobRecord{
		Path: existingPath, SHA256: "retried", Size: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.BlobID != existingTarget.ID || second.BlobPath != existingPath {
		t.Fatalf("existing-target retained message=%+v, want blob %d", second, existingTarget.ID)
	}
	if _, err := db.GetBlobForUser(ctx, owner.ID, secondRemote.ID); err != nil {
		t.Fatalf("store removed displaced metadata before filesystem cleanup could be queued: %v", err)
	}
}
