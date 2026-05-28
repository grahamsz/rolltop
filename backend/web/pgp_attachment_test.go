package web

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func TestPGPPublicKeyAttachmentCandidateRequiresPluginSmallASC(t *testing.T) {
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
	server := &Server{store: db}
	view := threadMessageView{
		Message:     store.MessageRecord{ID: 1, UserID: user.ID},
		SenderEmail: "alice@example.test",
		Attachments: []store.Attachment{
			{ID: 1, UserID: user.ID, MessageID: 1, Filename: "alice.asc", ContentType: "application/pgp-keys", Size: 2481},
			{ID: 2, UserID: user.ID, MessageID: 1, Filename: "large.asc", ContentType: "application/pgp-keys", Size: 20 * 1024},
			{ID: 3, UserID: user.ID, MessageID: 1, Filename: "OpenPGP_signature.asc", ContentType: "application/pgp-signature", Size: 677},
			{ID: 4, UserID: user.ID, MessageID: 1, Filename: "note.txt", ContentType: "text/plain", Size: 12},
		},
	}
	disabled := server.apiThreadMessages(ctx, user.ID, []threadMessageView{view})
	if disabled[0].Attachments[0].PGPPublicKeyCandidate {
		t.Fatal("PGP attachment candidate was exposed while plugin is disabled")
	}
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	server.pluginManifests, server.backendPlugins = testClientSidePGPBackendPlugins(t)
	enabled := server.apiThreadMessages(ctx, user.ID, []threadMessageView{view})
	if !enabled[0].Attachments[0].PGPPublicKeyCandidate {
		t.Fatal("PGP public-key attachment was not marked as a candidate")
	}
	for _, att := range enabled[0].Attachments[1:] {
		if att.PGPPublicKeyCandidate {
			t.Fatalf("attachment %q was unexpectedly marked as a PGP key candidate", att.Filename)
		}
	}
}

func TestThreadMessageHydratesMissingAttachmentMetadataFromRawBlob(t *testing.T) {
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
	raw := "From: Alice <alice@example.test>\r\n" +
		"To: Me <me@example.test>\r\n" +
		"Subject: Key\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"rolltop-test\"\r\n" +
		"\r\n" +
		"--rolltop-test\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"hello\r\n" +
		"--rolltop-test\r\n" +
		"Content-Type: application/pgp-keys; name=\"alice.asc\"\r\n" +
		"Content-Disposition: attachment; filename=\"alice.asc\"\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n" +
		"\r\n" +
		"-----BEGIN PGP PUBLIC KEY BLOCK-----\r\n" +
		"\r\n" +
		"test\r\n" +
		"-----END PGP PUBLIC KEY BLOCK-----\r\n" +
		"--rolltop-test--\r\n"
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
		UserID:         user.ID,
		AccountID:      account.ID,
		MailboxID:      mailbox.ID,
		BlobID:         blobRec.ID,
		ThreadKey:      "key-thread",
		Subject:        "Key",
		FromAddr:       "Alice <alice@example.test>",
		ToAddr:         "Me <me@example.test>",
		Date:           time.Now(),
		InternalDate:   time.Now(),
		UID:            42,
		Size:           saved.Size,
		BlobPath:       saved.Path,
		IsRead:         true,
		HasAttachments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.ListAttachmentsForMessage(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("precondition failed: got %d attachment rows", len(before))
	}
	server := &Server{store: db, blobs: blobStore}
	views, _, err := server.threadViewsForMessage(ctx, currentUser{User: user}, msg, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Attachments) != 1 {
		t.Fatalf("thread attachments = %#v", views)
	}
	if got := views[0].Attachments[0].Filename; got != "alice.asc" {
		t.Fatalf("attachment filename = %q", got)
	}
	after, err := db.ListAttachmentsForMessage(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Filename != "alice.asc" {
		t.Fatalf("stored attachments = %#v", after)
	}
}

func TestThreadMessageRepairsInlinePGPSignedStateFromRawBlob(t *testing.T) {
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
	raw := "From: Robot <robot@example.test>\r\n" +
		"To: Me <me@example.test>\r\n" +
		"Subject: Signed\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"\r\n" +
		"-----BEGIN PGP SIGNED MESSAGE-----\r\n" +
		"Hash: SHA1\r\n" +
		"\r\n" +
		"Signed body\r\n" +
		"-----BEGIN PGP SIGNATURE-----\r\n" +
		"\r\n" +
		"wrfakebase64\r\n" +
		"-----END PGP SIGNATURE-----\r\n"
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
		ThreadKey:    "signed-thread",
		Subject:      "Signed",
		FromAddr:     "Robot <robot@example.test>",
		ToAddr:       "Me <me@example.test>",
		Date:         time.Now(),
		InternalDate: time.Now(),
		UID:          43,
		Size:         saved.Size,
		BlobPath:     saved.Path,
		IsRead:       true,
		IsSigned:     false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, blobs: blobStore}
	server.pluginManifests, server.backendPlugins = testClientSidePGPBackendPlugins(t)
	views, repaired, err := server.threadViewsForMessage(ctx, currentUser{User: user}, msg, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !repaired.IsSigned || len(views) != 1 || !views[0].Message.IsSigned {
		t.Fatalf("signed state was not repaired: repaired=%t views=%#v", repaired.IsSigned, views)
	}
	stored, err := db.GetMessageForUser(ctx, user.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.IsSigned {
		t.Fatal("stored message signed state was not updated")
	}
}
