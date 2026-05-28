package web

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

func TestDisplayBodiesShowsPGPCiphertextWhenPluginDisabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993, Username: user.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join([]string{
		"From: Alice <alice@example.test>",
		"To: Me <me@example.test>",
		"Subject: Encrypted",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"-----BEGIN PGP MESSAGE-----",
		"",
		"wcDMAciphertext",
		"-----END PGP MESSAGE-----",
	}, "\r\n")
	blobStore := blob.New(dir)
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, 42, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	blobRec, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: saved.Path, SHA256: saved.SHA256, Size: saved.Size})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:       user.ID,
		AccountID:    account.ID,
		MailboxID:    mailbox.ID,
		BlobID:       blobRec.ID,
		Subject:      "Encrypted",
		FromAddr:     "Alice <alice@example.test>",
		ToAddr:       "Me <me@example.test>",
		Date:         time.Now(),
		InternalDate: time.Now(),
		UID:          42,
		Size:         saved.Size,
		BlobPath:     saved.Path,
		IsEncrypted:  true,
		IsRead:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, blobs: blobStore}
	_, text, previewOnly := server.displayBodiesForMessage(ctx, user.ID, msg)
	if previewOnly {
		t.Fatal("PGP ciphertext display unexpectedly fell back to preview only")
	}
	if !strings.Contains(text, "-----BEGIN PGP MESSAGE-----") || !strings.Contains(text, "wcDMAciphertext") {
		t.Fatalf("display text did not include ciphertext: %q", text)
	}
}
