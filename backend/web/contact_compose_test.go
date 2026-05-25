// File overview: Tests for compose behavior that depends on contacts and identities.

package web

import (
	"context"
	"path/filepath"
	"testing"

	"mailmirror/backend/blob"
	"mailmirror/backend/smtpclient"
	"mailmirror/backend/store"
)

type captureSender struct {
	msg smtpclient.Message
}

func (s *captureSender) Send(_ context.Context, _ store.MailAccount, msg smtpclient.Message) ([]byte, error) {
	s.msg = msg
	raw, _, err := smtpclient.BuildRaw(msg)
	return raw, err
}

func TestSendComposeRejectsOtherUserFromIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:                user.ID,
		Email:                 "me@example.test",
		Host:                  "imap.example.test",
		Port:                  993,
		Username:              "me@example.test",
		EncryptedPassword:     "encrypted",
		UseTLS:                true,
		SMTPHost:              "smtp.example.test",
		SMTPPort:              587,
		SMTPUsername:          "me@example.test",
		EncryptedSMTPPassword: "encrypted",
		SMTPUseTLS:            true,
		Mailbox:               "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.ID == 0 {
		t.Fatal("missing account")
	}
	ownIdentity, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Personal Me",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []store.ContactEmail{{Email: "alias@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := db.CreateContact(ctx, other.ID, store.Contact{
		DisplayName: "Other Identity",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []store.ContactEmail{{Email: "other-alias@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &captureSender{}
	server := &Server{store: db, blobs: blob.New(dir), sender: sender}
	cu := currentUser{User: user}
	if _, err := server.sendCompose(ctx, cu, composeForm{
		To:             "recipient@example.test",
		Subject:        "Nope",
		Body:           "body",
		FromIdentityID: otherIdentity.Emails[0].ID,
	}); err == nil {
		t.Fatal("expected other user's identity to be rejected")
	}
	if _, err := server.sendCompose(ctx, cu, composeForm{
		To:             "recipient@example.test",
		Subject:        "Hello",
		Body:           "body",
		FromIdentityID: ownIdentity.Emails[0].ID,
	}); err != nil {
		t.Fatal(err)
	}
	if sender.msg.From != `"Personal Me" <alias@example.test>` {
		t.Fatalf("from = %q", sender.msg.From)
	}
}

func TestReplyComposeSelectsIdentityMatchingRecipient(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "primary@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             "primary@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          "primary@example.test",
		EncryptedPassword: "encrypted",
		UseTLS:            true,
		Mailbox:           "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blobRec, err := db.CreateBlob(ctx, store.BlobRecord{UserID: user.ID, Kind: "message", Path: "users/1/blobs/accounts/1/mailboxes/INBOX/uid-1.eml", SHA256: "hash", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Primary Me",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []store.ContactEmail{{Email: "primary@example.test", IsPrimary: true}},
	}); err != nil {
		t.Fatal(err)
	}
	alias, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Alias Me",
		IsMe:        true,
		Emails:      []store.ContactEmail{{Email: "alias@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID:          user.ID,
		AccountID:       account.ID,
		MailboxID:       mailbox.ID,
		BlobID:          blobRec.ID,
		MessageIDHeader: "<incoming@example.test>",
		FromAddr:        "Sender <sender@example.test>",
		ToAddr:          "Alias Me <alias@example.test>",
		Subject:         "Alias thread",
		UID:             1,
		BlobPath:        blobRec.Path,
		BodyText:        "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db}
	form := replyComposeForm(msg, []store.MessageRecord{msg}, server.ownAddresses(ctx, user))
	form.FromIdentityID = server.replyFromIdentityID(ctx, currentUser{User: user}, msg, []store.MessageRecord{msg})
	if form.FromIdentityID != alias.Emails[0].ID {
		t.Fatalf("from_identity_id = %d, want %d", form.FromIdentityID, alias.Emails[0].ID)
	}
}
