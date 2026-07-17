// File overview: Tests for compose behavior that depends on contacts and identities.

package web

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/plugins"
	"rolltop/backend/search"
	"rolltop/backend/smtpclient"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
	"rolltop/plugins/client_side_pgp/backend/keystore"
)

const composeAutocryptPublicKey = `-----BEGIN PGP PUBLIC KEY BLOCK-----

AQIDBAUGBwg=
-----END PGP PUBLIC KEY BLOCK-----`

type captureSender struct {
	msg    smtpclient.Message
	count  int
	onSend func()
}

func (s *captureSender) Send(_ context.Context, _ store.MailAccount, msg smtpclient.Message) ([]byte, error) {
	if s.onSend != nil {
		s.onSend()
	}
	s.msg = msg
	s.count++
	raw, _, err := smtpclient.BuildRaw(msg)
	return raw, err
}

func captureExtraHeader(msg smtpclient.Message, name string) string {
	for _, header := range msg.ExtraHeaders {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

type captureAppendFetcher struct {
	syncer.Fetcher
	nextUID     uint32
	uidValidity uint32
	flags       []string
	onAppend    func()
}

type blockingMailboxStatusFetcher struct {
	*captureAppendFetcher
	status  syncer.MailboxStatus
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingMailboxStatusFetcher) MailboxStatus(ctx context.Context, _ store.MailAccount, _ string) (syncer.MailboxStatus, error) {
	f.once.Do(func() { close(f.started) })
	select {
	case <-f.release:
		return f.status, nil
	case <-ctx.Done():
		return syncer.MailboxStatus{}, ctx.Err()
	}
}

func (f *captureAppendFetcher) AppendMessage(_ context.Context, _ store.MailAccount, mailbox string, raw []byte, _ string, date time.Time) (syncer.FetchedMessage, error) {
	if f.onAppend != nil {
		f.onAppend()
	}
	if f.nextUID == 0 {
		f.nextUID = 1
	}
	msg := syncer.FetchedMessage{Mailbox: mailbox, UID: f.nextUID, UIDValidity: f.uidValidity, InternalDate: date, Size: int64(len(raw)), Flags: []string{"\\Seen"}, Raw: raw}
	f.nextUID++
	return msg, nil
}

func (f *captureAppendFetcher) AppendMessageWithFlags(_ context.Context, _ store.MailAccount, mailbox string, raw []byte, _ string, date time.Time, flags []string) (syncer.FetchedMessage, error) {
	if f.onAppend != nil {
		f.onAppend()
	}
	if f.nextUID == 0 {
		f.nextUID = 1
	}
	f.flags = append([]string(nil), flags...)
	msg := syncer.FetchedMessage{Mailbox: mailbox, UID: f.nextUID, UIDValidity: f.uidValidity, InternalDate: date, Size: int64(len(raw)), Flags: flags, Raw: raw}
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

func TestSendComposeHoldsForegroundDuringSMTP(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, false)
	runner := syncer.NewRunner(server.syncer)
	server.syncRunner = runner

	heldForeground := false
	sender.onSend = func() {
		heldForeground = runner.IsRunning(user.ID)
	}
	if _, err := server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "Reserved delivery",
		Body:           "body",
		FromIdentityID: fromID,
	}); err != nil {
		t.Fatal(err)
	}
	if !heldForeground {
		t.Fatal("SMTP send ran without a foreground reservation")
	}
	if runner.IsRunning(user.ID) {
		t.Fatal("compose foreground reservation remained active after send")
	}
	if completedAt := sentImportCompletedAtForSubject(t, ctx, server.store, user.ID, "Reserved delivery"); completedAt <= 0 {
		t.Fatalf("Sent import_completed_at=%d, want completed timestamp", completedAt)
	}
}

func TestSendComposeLeavesSentImportPendingWhenSearchIndexFails(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, _, _ := setupAutocryptComposeTest(t, ctx, false)
	searchService, err := search.Open(filepath.Join(t.TempDir(), "sent-search.bleve"))
	if err != nil {
		t.Fatal(err)
	}
	if err := searchService.Close(); err != nil {
		t.Fatal(err)
	}
	server.search = searchService

	_, err = server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "Pending sent import",
		Body:           "body",
		FromIdentityID: fromID,
	})
	if err == nil {
		t.Fatal("sendCompose succeeded with a closed search index")
	}
	if completedAt := sentImportCompletedAtForSubject(t, ctx, server.store, user.ID, "Pending sent import"); completedAt != 0 {
		t.Fatalf("failed Sent import_completed_at=%d, want pending 0", completedAt)
	}
}

func sentImportCompletedAtForSubject(t *testing.T, ctx context.Context, db *store.Store, userID int64, subject string) int64 {
	t.Helper()
	var completedAt int64
	if err := db.DB().QueryRowContext(ctx, `SELECT import_completed_at FROM messages
		WHERE user_id = ? AND subject = ?`, userID, subject).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	return completedAt
}

func TestSendComposePreemptsMailboxGenerationRecovery(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, false)
	accounts, err := server.store.ListMailAccountsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("mail account count = %d, want 1", len(accounts))
	}
	sentMailbox, err := server.store.GetMailbox(ctx, user.ID, accounts[0].ID, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	const targetUIDValidity = 901
	now := time.Now().Unix()
	if _, err := server.store.DB().ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, arrival_uid_floor, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		user.ID, accounts[0].ID, sentMailbox.ID, targetUIDValidity, 1, now, now); err != nil {
		t.Fatal(err)
	}

	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	fetcher := &blockingMailboxStatusFetcher{
		captureAppendFetcher: &captureAppendFetcher{nextUID: 2, uidValidity: targetUIDValidity},
		status:               syncer.MailboxStatus{UIDNext: 2, UIDValidity: targetUIDValidity},
		started:              recoveryStarted,
		release:              releaseRecovery,
	}
	service := &syncer.Service{Store: server.store, Blobs: server.blobs, Fetcher: fetcher}
	runnerCtx, stopRunner := context.WithCancel(context.Background())
	defer stopRunner()
	runner := syncer.NewRunnerWithContext(runnerCtx, service)
	server.syncer = service
	server.syncRunner = runner
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-recoveryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox generation recovery did not start")
	}

	sendCtx, cancelSend := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancelSend()
	_, err = server.sendCompose(sendCtx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "Foreground delivery",
		Body:           "body",
		FromIdentityID: fromID,
	})
	if err != nil {
		t.Fatalf("sendCompose error = %v", err)
	}
	if sender.count != 1 {
		t.Fatalf("SMTP send count = %d, want 1", sender.count)
	}

	close(releaseRecovery)
	deadline := time.Now().Add(2 * time.Second)
	for runner.IsRunning(user.ID) {
		if time.Now().After(deadline) {
			t.Fatal("mailbox generation recovery did not release")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBeginComposeForegroundOperationHasBoundedWait(t *testing.T) {
	const userID = int64(47)
	runner := syncer.NewRunner(nil)
	server := &Server{syncRunner: runner}
	finishFirst, err := runner.BeginForegroundOperation(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	defer finishFirst()

	started := time.Now()
	finishSecond, err := server.beginComposeForegroundOperationWithin(context.Background(), userID, 25*time.Millisecond)
	if finishSecond != nil {
		finishSecond()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("begin compose foreground operation error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("begin compose foreground operation took %s, want bounded wait", elapsed)
	}
}

func TestSendComposeDoesNotSendWhenForegroundReservationIsCanceled(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, false)
	runner := syncer.NewRunner(nil)
	server.syncRunner = runner
	finishBlockingOperation, err := runner.BeginForegroundOperation(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer finishBlockingOperation()

	sendCtx, cancelSend := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelSend()
	_, err = server.sendCompose(sendCtx, currentUser{User: user}, composeForm{
		To:             "recipient@example.test",
		Subject:        "Canceled reservation",
		Body:           "body",
		FromIdentityID: fromID,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sendCompose error = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "message was not sent") {
		t.Fatalf("sendCompose error = %q, want explicit not-sent status", err)
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
	drafts, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Drafts")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxLastUID(ctx, user.ID, drafts.ID, 50); err != nil {
		t.Fatal(err)
	}
	const (
		staleDraftsUIDValidity = 222
		draftsUIDValidity      = 333
	)
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, drafts.ID, 0, 0, 51, staleDraftsUIDValidity); err != nil {
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
	fetcher := &captureAppendFetcher{nextUID: 73, uidValidity: draftsUIDValidity}
	blobStore := blob.New(dir)
	service := &syncer.Service{Store: db, Blobs: blobStore, Fetcher: fetcher}
	// The runner only supplies the foreground reservation in this unit test.
	// Keeping recovery on the service callback avoids launching a background
	// rebuild with this deliberately append-only fetcher.
	runner := syncer.NewRunner(nil)
	appendHeldForeground := false
	recoverySignaledDuringForeground := false
	fetcher.onAppend = func() {
		appendHeldForeground = runner.IsRunning(user.ID)
	}
	// Observe the exact reset signal point without starting an asynchronous
	// recovery worker against this intentionally minimal compose fetcher.
	service.MailboxGenerationRecoveryStarted = func(userID int64) {
		recoverySignaledDuringForeground = runner.IsRunning(userID)
	}
	server := &Server{store: db, blobs: blobStore, syncer: service, syncRunner: runner}
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
	drafts, err = db.GetMailboxForUser(ctx, user.ID, drafts.ID)
	if err != nil {
		t.Fatal(err)
	}
	if drafts.LastUID != 0 {
		t.Fatalf("draft generation reset last_uid=%d, want 0", drafts.LastUID)
	}
	draftUIDValidity, err := db.GetMessageUIDValidityForUser(ctx, user.ID, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if draftUIDValidity != draftsUIDValidity {
		t.Fatalf("draft uid_validity = %d, want APPEND generation %d", draftUIDValidity, draftsUIDValidity)
	}
	arrivalUIDFloor, err := db.MailboxGenerationRebuildArrivalUIDFloor(ctx, user.ID, account.ID, drafts.ID, draftsUIDValidity)
	if err != nil || arrivalUIDFloor != 74 {
		t.Fatalf("draft rebuild arrival floor=%d err=%v, want 74/nil", arrivalUIDFloor, err)
	}
	if !appendHeldForeground || !recoverySignaledDuringForeground {
		t.Fatalf("compose foreground append=%t reset_signal=%t, want both true", appendHeldForeground, recoverySignaledDuringForeground)
	}
	if runner.IsRunning(user.ID) {
		t.Fatal("compose foreground reservation remained active after draft save")
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
			autocrypt := captureExtraHeader(sender.msg, "Autocrypt")
			if tc.wantAutocryptAddr != "" && !strings.Contains(autocrypt, "addr="+tc.wantAutocryptAddr) {
				t.Fatalf("Autocrypt header = %q, want addr %q", autocrypt, tc.wantAutocryptAddr)
			}
			if tc.wantAutocryptAddr != "" && !strings.Contains(autocrypt, "keydata=AQIDBAUGBwg=") {
				t.Fatalf("Autocrypt header missing keydata: %q", autocrypt)
			}
			if tc.wantAutocryptAddr == "" && autocrypt != "" {
				t.Fatalf("Autocrypt header = %q, want empty", autocrypt)
			}
		})
	}
}

func TestSendComposeSecurityMIMEEncryptedForm(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, true)
	if _, err := server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:                "recipient@example.test",
		Subject:           "PGP/MIME",
		Body:              "-----BEGIN PGP MESSAGE-----\n\nciphertext\n-----END PGP MESSAGE-----",
		FromIdentityID:    fromID,
		SecurityEncrypted: true,
		SecurityMIME:      true,
	}); err != nil {
		t.Fatal(err)
	}
	if sender.msg.MIMEBodyOverride == nil || !strings.Contains(sender.msg.MIMEBodyOverride.ContentType, "multipart/encrypted") {
		t.Fatal("sendCompose did not prepare encrypted PGP form as a MIME override")
	}
}

func TestSendComposeSecurityMIMESignedForm(t *testing.T) {
	ctx := context.Background()
	server, user, fromID, sender, _ := setupAutocryptComposeTest(t, ctx, true)
	if _, err := server.sendCompose(ctx, currentUser{User: user}, composeForm{
		To:                "recipient@example.test",
		Subject:           "PGP/MIME signed",
		Body:              "Content-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\nSigned text\r\n",
		SecuritySignature: "-----BEGIN PGP SIGNATURE-----\n\nsignature\n-----END PGP SIGNATURE-----",
		FromIdentityID:    fromID,
		SecuritySigned:    true,
		SecurityMIME:      true,
	}); err != nil {
		t.Fatal(err)
	}
	if sender.msg.MIMEBodyOverride == nil || !strings.Contains(sender.msg.MIMEBodyOverride.ContentType, "multipart/signed") {
		t.Fatal("sendCompose did not prepare signed PGP form as a MIME override")
	}
	if !strings.Contains(sender.msg.MIMEBodyOverride.Body, "-----BEGIN PGP SIGNATURE-----") {
		t.Fatal("sendCompose did not preserve the detached PGP/MIME signature")
	}
	if strings.Contains(sender.msg.MIMEBodyOverride.ContentType, "multipart/encrypted") {
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
	if _, err := keystore.UpsertIdentityPrivateKey(ctx, db, store.IdentityPGPPrivateKey{
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
	if enablePGPPlugin {
		server.pluginManifests, server.backendPlugins = testClientSidePGPBackendPlugins(t)
	}
	return server, user, contact.Emails[0].ID, sender, identities[0]
}
