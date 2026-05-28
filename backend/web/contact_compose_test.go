// File overview: Tests for compose behavior that depends on contacts and identities.

package web

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/plugins"
	"rolltop/backend/smtpclient"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

const composeAutocryptPublicKey = `-----BEGIN PGP PUBLIC KEY BLOCK-----

AQIDBAUGBwg=
-----END PGP PUBLIC KEY BLOCK-----`

type captureSender struct {
	msg   smtpclient.Message
	count int
}

func (s *captureSender) Send(_ context.Context, _ store.MailAccount, msg smtpclient.Message) ([]byte, error) {
	s.msg = msg
	s.count++
	raw, _, err := smtpclient.BuildRaw(msg)
	return raw, err
}

type captureAppendFetcher struct {
	syncer.Fetcher
	nextUID uint32
	flags   []string
}

func (f *captureAppendFetcher) AppendMessage(_ context.Context, _ store.MailAccount, mailbox string, raw []byte, _ string, date time.Time) (syncer.FetchedMessage, error) {
	if f.nextUID == 0 {
		f.nextUID = 1
	}
	msg := syncer.FetchedMessage{Mailbox: mailbox, UID: f.nextUID, InternalDate: date, Size: int64(len(raw)), Flags: []string{"\\Seen"}, Raw: raw}
	f.nextUID++
	return msg, nil
}

func (f *captureAppendFetcher) AppendMessageWithFlags(_ context.Context, _ store.MailAccount, mailbox string, raw []byte, _ string, date time.Time, flags []string) (syncer.FetchedMessage, error) {
	if f.nextUID == 0 {
		f.nextUID = 1
	}
	f.flags = append([]string(nil), flags...)
	msg := syncer.FetchedMessage{Mailbox: mailbox, UID: f.nextUID, InternalDate: date, Size: int64(len(raw)), Flags: flags, Raw: raw}
	f.nextUID++
	return msg, nil
}

func TestSendComposeRequiresSentRoleBeforeSMTP(t *testing.T) {
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
	if _, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             "me@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          "me@example.test",
		EncryptedPassword: "encrypted",
		UseTLS:            true,
		Mailbox:           "INBOX",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSMTPAccount(ctx, store.SMTPAccount{UserID: user.ID, Label: "SMTP", Host: "smtp.example.test", Port: 587, Username: "me@example.test", EncryptedPassword: "encrypted", UseTLS: true}); err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Me",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []store.ContactEmail{{Email: "me@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &captureSender{}
	server := &Server{store: db, blobs: blob.New(dir), sender: sender, syncer: &syncer.Service{Fetcher: &captureAppendFetcher{}}}
	_, err = server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "No sent folder",
		Body:           "body",
		FromIdentityID: contact.Emails[0].ID,
	})
	if err == nil || !strings.Contains(err.Error(), "Sent folder role") {
		t.Fatalf("sendCompose error = %v, want missing Sent role", err)
	}
	if sender.count != 0 {
		t.Fatalf("SMTP send count = %d, want 0", sender.count)
	}
}

func TestSaveComposeDraftAppendsToDraftsMailbox(t *testing.T) {
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
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             "me@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          "me@example.test",
		EncryptedPassword: "encrypted",
		UseTLS:            true,
		Mailbox:           "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Drafts"); err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Me",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []store.ContactEmail{{Email: "me@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &captureAppendFetcher{}
	server := &Server{store: db, blobs: blob.New(dir), syncer: &syncer.Service{Fetcher: fetcher}}
	draft, err := server.saveComposeDraft(ctx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Bcc:            "hidden@example.test",
		Subject:        "Unfinished",
		Body:           "draft body",
		FromIdentityID: contact.Emails[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.ID == 0 || draft.MailboxID == 0 {
		t.Fatalf("invalid draft message: %+v", draft)
	}
	if strings.Join(fetcher.flags, ",") != "\\Draft" {
		t.Fatalf("append flags = %v, want Draft", fetcher.flags)
	}
	form := server.draftComposeFormForMessage(ctx, currentUser{User: user}, draft)
	if form.Bcc != "<hidden@example.test>" {
		t.Fatalf("draft bcc = %q", form.Bcc)
	}
}

func TestSendComposeAutocryptHeaderRequiresPluginAndIdentitySetting(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enablePGPPlugin   bool
		autocryptEnabled  bool
		wantAutocryptAddr string
	}{
		{name: "plugin enabled", enablePGPPlugin: true, autocryptEnabled: true, wantAutocryptAddr: "me@example.test"},
		{name: "plugin disabled", enablePGPPlugin: false, autocryptEnabled: true},
		{name: "identity disabled", enablePGPPlugin: true, autocryptEnabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			server, user, fromID, sender, identity := setupAutocryptComposeTest(t, ctx, tc.enablePGPPlugin)
			if identity.AutocryptEnabled != tc.autocryptEnabled {
				identity.AutocryptEnabled = tc.autocryptEnabled
				var err error
				identity, err = server.store.UpdateMailIdentityForUser(ctx, user.ID, identity)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := server.sendCompose(ctx, currentUser{User: user}, composeForm{
				To:             "recipient@example.test",
				Subject:        "Autocrypt",
				Body:           "body",
				FromIdentityID: fromID,
			}); err != nil {
				t.Fatal(err)
			}
			if sender.msg.AutocryptAddr != tc.wantAutocryptAddr {
				t.Fatalf("AutocryptAddr = %q, want %q", sender.msg.AutocryptAddr, tc.wantAutocryptAddr)
			}
			if tc.wantAutocryptAddr != "" && sender.msg.AutocryptKeyData != "AQIDBAUGBwg=" {
				t.Fatalf("AutocryptKeyData = %q", sender.msg.AutocryptKeyData)
			}
			if tc.wantAutocryptAddr == "" && sender.msg.AutocryptKeyData != "" {
				t.Fatalf("AutocryptKeyData = %q, want empty", sender.msg.AutocryptKeyData)
			}
		})
	}
}

func TestSendComposePGPMIMEEncryptedForm(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, true)
	if _, err := server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "PGP/MIME",
		Body:           "-----BEGIN PGP MESSAGE-----\n\nciphertext\n-----END PGP MESSAGE-----",
		FromIdentityID: fromID,
		PGPEncrypted:   true,
		PGPMIME:        true,
	}); err != nil {
		t.Fatal(err)
	}
	if !sender.msg.PGPMIMEEncrypted {
		t.Fatal("sendCompose did not mark encrypted PGP form as PGP/MIME")
	}
}

func TestSendComposePGPMIMESignedForm(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, true)
	if _, err := server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "PGP/MIME signed",
		Body:           "Content-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\nSigned text\r\n",
		PGPSignature:   "-----BEGIN PGP SIGNATURE-----\n\nsignature\n-----END PGP SIGNATURE-----",
		FromIdentityID: fromID,
		PGPSigned:      true,
		PGPMIME:        true,
	}); err != nil {
		t.Fatal(err)
	}
	if !sender.msg.PGPMIMESigned {
		t.Fatal("sendCompose did not mark signed PGP form as PGP/MIME")
	}
	if sender.msg.PGPMIMESignature == "" {
		t.Fatal("sendCompose did not preserve the detached PGP/MIME signature")
	}
	if sender.msg.PGPMIMEEncrypted {
		t.Fatal("signed-only PGP/MIME form was marked encrypted")
	}
}

func TestSendComposeRejectsOtherUserFromIdentity(t *testing.T) {
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
	if _, err := db.CreateSMTPAccount(ctx, store.SMTPAccount{UserID: user.ID, Label: "SMTP", Host: "smtp.example.test", Port: 587, Username: "me@example.test", EncryptedPassword: "encrypted", UseTLS: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Sent"); err != nil {
		t.Fatal(err)
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
	server := &Server{store: db, blobs: blob.New(dir), sender: sender, syncer: &syncer.Service{Fetcher: &captureAppendFetcher{}}}
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
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
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

func setupAutocryptComposeTest(t *testing.T, ctx context.Context, enablePGPPlugin bool) (*Server, store.User, int64, *captureSender, store.MailIdentity) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if enablePGPPlugin {
		if err := db.SetPluginEnabled(ctx, plugins.ClientSidePGP, true); err != nil {
			t.Fatal(err)
		}
	}
	user, err := db.CreateUser(ctx, "me@example.test", "Me", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:            user.ID,
		Email:             "me@example.test",
		Host:              "imap.example.test",
		Port:              993,
		Username:          "me@example.test",
		EncryptedPassword: "encrypted",
		UseTLS:            true,
		Mailbox:           "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSMTPAccount(ctx, store.SMTPAccount{UserID: user.ID, Label: "SMTP", Host: "smtp.example.test", Port: 587, Username: "me@example.test", EncryptedPassword: "encrypted", UseTLS: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Sent"); err != nil {
		t.Fatal(err)
	}
	contact, err := db.CreateContact(ctx, user.ID, store.Contact{
		DisplayName: "Me",
		IsMe:        true,
		IsPrimary:   true,
		Emails:      []store.ContactEmail{{Email: "me@example.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	identities, err := db.ListMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 {
		t.Fatalf("identity count = %d, want 1", len(identities))
	}
	if _, err := db.UpsertIdentityPGPPrivateKey(ctx, store.IdentityPGPPrivateKey{
		UserID:              user.ID,
		IdentityID:          identities[0].ID,
		Label:               "Me key",
		Fingerprint:         "AABBCCDDEEFF00112233445566778899AABBCCDD",
		KeyID:               "66778899AABBCCDD",
		UserIDs:             "Me <me@example.test>",
		PublicKeyArmored:    composeAutocryptPublicKey,
		EncryptedPrivateKey: "encrypted",
		IsActiveSigning:     true,
		IsActiveEncryption:  true,
	}); err != nil {
		t.Fatal(err)
	}
	sender := &captureSender{}
	server := &Server{store: db, blobs: blob.New(dir), sender: sender, syncer: &syncer.Service{Fetcher: &captureAppendFetcher{}}}
	return server, user, contact.Emails[0].ID, sender, identities[0]
}
