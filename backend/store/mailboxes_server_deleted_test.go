// File overview: Tests for DeleteMailbox, the store helper the syncer uses to
// drop a local folder row after pruning the messages of a folder the IMAP
// server no longer reports.

package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDeleteMailboxRemovesFolderRow(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "folder-prune@example.test", "Folder Prune", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: "folder-prune@example.test", Host: "imap.example.test",
		Port: 993, Username: "folderprune", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mb, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "OldFolder")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMailbox(ctx, user.ID, mb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMailbox(ctx, user.ID, account.ID, "OldFolder"); !IsNotFound(err) {
		t.Fatalf("GetMailbox after delete: err = %v, want not found", err)
	}
	if err := db.DeleteMailbox(ctx, user.ID, mb.ID); !IsNotFound(err) {
		t.Fatalf("DeleteMailbox twice: err = %v, want not found", err)
	}
}
