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

func TestDisplayBodiesPersistsParsedBlobBody(t *testing.T) {
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
		"Subject: Cached body",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"This body came from the local blob store.",
	}, "\r\n")
	blobStore := blob.New(dir)
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, 43, []byte(raw))
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
		Subject:      "Cached body",
		FromAddr:     "Alice <alice@example.test>",
		ToAddr:       "Me <me@example.test>",
		Date:         time.Now(),
		InternalDate: time.Now(),
		UID:          43,
		Size:         saved.Size,
		BlobPath:     saved.Path,
		IsRead:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, blobs: blobStore}

	_, text, previewOnly := server.displayBodiesForMessage(ctx, user.ID, msg)

	if previewOnly {
		t.Fatal("display body unexpectedly fell back to preview only")
	}
	if !strings.Contains(text, "local blob store") {
		t.Fatalf("display text = %q", text)
	}
	updated, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated.BodyText, "local blob store") {
		t.Fatalf("persisted body text = %q", updated.BodyText)
	}
}

func TestDisplayBodiesRehydratesCompactedHTMLFromBlob(t *testing.T) {
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
		"Subject: Formatted",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><h1>Formatted Heading</h1><p><strong>Styled</strong> message body.</p></body></html>",
	}, "\r\n")
	blobStore := blob.New(dir)
	saved, err := blobStore.SaveRawMessage(user.ID, account.ID, mailbox.Name, 44, []byte(raw))
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
		Subject:      "Formatted",
		FromAddr:     "Alice <alice@example.test>",
		ToAddr:       "Me <me@example.test>",
		Date:         time.Now().Add(-2 * time.Hour),
		InternalDate: time.Now().Add(-2 * time.Hour),
		UID:          44,
		Size:         saved.Size,
		BlobPath:     saved.Path,
		BodyText:     "Formatted Heading Styled message body.",
		BodyHTML:     "<html><body><h1>Formatted Heading</h1><p><strong>Styled</strong> message body.</p></body></html>",
		IsSigned:     true,
		IsRead:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := db.CompactMessageBodiesBefore(ctx, time.Now(), 16, 10); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("compacted = %d, want 1", n)
	}
	compacted, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if compacted.BodyHTML != "" || !strings.HasSuffix(compacted.BodyText, " ...") {
		t.Fatalf("message was not compacted to preview text: html=%q text=%q", compacted.BodyHTML, compacted.BodyText)
	}
	envelope, err := db.GetMessageEnvelopeForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, blobs: blobStore}

	html, text, previewOnly := server.displayBodiesForMessage(ctx, user.ID, envelope)

	if previewOnly {
		t.Fatal("display body unexpectedly fell back to preview only")
	}
	if !strings.Contains(html, "<h1>Formatted Heading</h1>") || !strings.Contains(html, "<strong>Styled</strong>") {
		t.Fatalf("display HTML was not rehydrated from blob: %q", html)
	}
	normalizedText := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(normalizedText, "Styled message body") {
		t.Fatalf("display text = %q", text)
	}
	rehydrated, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rehydrated.BodyHTML, "<strong>Styled</strong>") {
		t.Fatalf("persisted body HTML was not restored: %q", rehydrated.BodyHTML)
	}
}
