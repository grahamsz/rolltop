// File overview: Tests for store schema, tenant isolation, threading, sync runs, preferences, onboarding, and identities.

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestOpenServerStoresMailDataInUserDatabase(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	db, err := OpenServer(filepath.Join(dataDir, "mailmirror.db"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "split@example.test", "Split", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{UserID: user.ID, Email: "split@example.test", Host: "imap.example.test", Port: 993, Username: "split", EncryptedPassword: "secret", UseTLS: true, Mailbox: "*"})
	if err != nil {
		t.Fatal(err)
	}
	if account.ID == 0 {
		t.Fatal("account was not created")
	}
	userDBPath := filepath.Join(dataDir, "users", strconv.FormatInt(user.ID, 10), "mailmirror.db")
	if _, err := os.Stat(userDBPath); err != nil {
		t.Fatalf("user database was not created: %v", err)
	}
	var systemMailTable string
	if err := db.DB().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'mail_accounts'`).Scan(&systemMailTable); err != ErrNotFound {
		t.Fatalf("system database mail_accounts table lookup err = %v, want not found", err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var userAccounts int
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_accounts WHERE user_id = ?`, user.ID).Scan(&userAccounts); err != nil {
		t.Fatal(err)
	}
	if userAccounts != 1 {
		t.Fatalf("user database has %d mail accounts, want 1", userAccounts)
	}
	accounts, err := db.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].UserID != user.ID || accounts[0].ID != account.ID {
		t.Fatalf("ListAccounts = %+v", accounts)
	}
}

func TestCreateBlobIsIdempotentForUserPath(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "blob@example.test", "Blob", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	first, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID,
		Kind:   "message",
		Path:   "users/1/blobs/accounts/1/mailboxes/INBOX/uid-3449-deadbeef.eml",
		SHA256: "deadbeef",
		Size:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID,
		Kind:   "message",
		Path:   first.Path,
		SHA256: "deadbeef",
		Size:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same blob row, got first=%d second=%d", first.ID, second.ID)
	}
}

func TestThreadMessagesForUserUsesReferencesAndSubjectFallback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)

	first, err := db.CreateMessage(ctx, CreateMessage{
		UserID:          user.ID,
		AccountID:       account.ID,
		MailboxID:       mailbox.ID,
		BlobID:          blob.ID,
		MessageIDHeader: "<root@example.test>",
		Subject:         "Project Update",
		Date:            time.Now().Add(-time.Hour),
		UID:             1,
		BlobPath:        blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := db.CreateMessage(ctx, CreateMessage{
		UserID:           user.ID,
		AccountID:        account.ID,
		MailboxID:        mailbox.ID,
		BlobID:           blob.ID,
		MessageIDHeader:  "<reply@example.test>",
		ReferencesHeader: "<root@example.test>",
		Subject:          "Re: Project Update",
		Date:             time.Now(),
		UID:              2,
		BlobPath:         blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := db.ListThreadMessagesForUser(ctx, user.ID, reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 2 || thread[0].ID != first.ID || thread[1].ID != reply.ID {
		t.Fatalf("thread = %+v", thread)
	}
}

func TestBackfillThreadHeadersFromBlobsRepairsRowsMissingThreadHeaders(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)

	root, err := db.CreateMessage(ctx, CreateMessage{
		UserID:          user.ID,
		AccountID:       account.ID,
		MailboxID:       mailbox.ID,
		BlobID:          blob.ID,
		MessageIDHeader: "<root@example.test>",
		Subject:         "Recovered Thread",
		Date:            time.Now().Add(-time.Hour),
		UID:             1,
		BlobPath:        blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	replyPath := "users/1/blobs/accounts/1/mailboxes/INBOX/uid-2.eml"
	replyBlob, err := db.CreateBlob(ctx, BlobRecord{UserID: user.ID, Kind: "message", Path: replyPath, SHA256: "feed", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := db.CreateMessage(ctx, CreateMessage{
		UserID:          user.ID,
		AccountID:       account.ID,
		MailboxID:       mailbox.ID,
		BlobID:          replyBlob.ID,
		MessageIDHeader: "<reply@example.test>",
		Subject:         "Re: Recovered Thread",
		Date:            time.Now(),
		UID:             2,
		BlobPath:        replyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dataDir, replyPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, replyPath), []byte("From sender@example.test Sat Jan 01 00:00:00 2026\r\nMessage-ID: <reply@example.test>\r\nIn-Reply-To: <root@example.test>\r\nReferences: <root@example.test>\r\nSubject: Re: Recovered Thread\r\n\r\nbody is ignored\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET in_reply_to = '', references_header = '', thread_key = ?, thread_headers_checked_at = 0 WHERE id = ?`, ThreadKey("<reply@example.test>", "", "", "Re: Recovered Thread"), reply.ID); err != nil {
		t.Fatal(err)
	}

	checked, updated, err := db.BackfillThreadHeadersFromBlobs(ctx, dataDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 || updated != 1 {
		t.Fatalf("checked=%d updated=%d", checked, updated)
	}
	repaired, err := db.GetMessageForUser(ctx, user.ID, reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := db.ListThreadMessagesForUser(ctx, user.ID, repaired)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 2 || thread[0].ID != root.ID || thread[1].ID != reply.ID {
		t.Fatalf("thread = %+v", thread)
	}
}

func TestReadSenderStatsAreUserScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, mailbox, blob := testMailbox(t, ctx, db)
	other, err := db.CreateUser(ctx, "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.UpsertMailAccount(ctx, MailAccount{UserID: other.ID, Email: "other@example.test", Host: "imap.example.test", Port: 993, Username: "other", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailbox(ctx, other.ID, otherAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	otherBlob, err := db.CreateBlob(ctx, BlobRecord{UserID: other.ID, Kind: "message", Path: "users/2/blobs/accounts/1/mailboxes/INBOX/uid-1.eml", SHA256: "bead", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessage{UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID, FromAddr: "Known <known@example.test>", Subject: "a", Date: time.Now(), UID: 1, BlobPath: blob.Path, IsRead: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMessage(ctx, CreateMessage{UserID: other.ID, AccountID: otherAccount.ID, MailboxID: otherMailbox.ID, BlobID: otherBlob.ID, FromAddr: "Other <other@example.test>", Subject: "b", Date: time.Now(), UID: 1, BlobPath: otherBlob.Path, IsRead: true}); err != nil {
		t.Fatal(err)
	}
	stats, err := db.ListReadSenderStatsForUser(ctx, user.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Sender != "known@example.test" {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestSyncRunStoresLatestNewMessageDetails(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, _, _ := testMailbox(t, ctx, db)

	run, err := db.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	progress := SyncProgress{
		MessagesSeen:     1,
		MessagesStored:   1,
		NewMessages:      1,
		LatestNewFrom:    "Alice Example <alice@example.test>",
		LatestNewSubject: "Quarterly update",
		MessagesTotal:    1,
		MailboxesDone:    1,
		MailboxesTotal:   1,
		CurrentMailbox:   "INBOX",
		CurrentUID:       42,
	}
	if err := db.UpdateSyncRunProgress(ctx, user.ID, run.ID, progress); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LatestNewFrom != progress.LatestNewFrom || stored.LatestNewSubject != progress.LatestNewSubject {
		t.Fatalf("latest notification details = %q/%q", stored.LatestNewFrom, stored.LatestNewSubject)
	}

	if err := db.FinishSyncRun(ctx, user.ID, run.ID, "ok", progress, ""); err != nil {
		t.Fatal(err)
	}
	finished, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.LatestNewFrom != progress.LatestNewFrom || finished.LatestNewSubject != progress.LatestNewSubject {
		t.Fatalf("finished notification details = %q/%q", finished.LatestNewFrom, finished.LatestNewSubject)
	}
}

func TestListSyncRunsForUserCollapsesNoopFolderRuns(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, _, _ := testMailbox(t, ctx, db)

	createRun := func(mailbox string, stored int, status string) SyncRun {
		run, err := db.CreateSyncRun(ctx, user.ID, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		progress := SyncProgress{
			MessagesSeen:   stored,
			MessagesStored: stored,
			MessagesTotal:  stored,
			MailboxesDone:  1,
			MailboxesTotal: 1,
			CurrentMailbox: mailbox,
			CurrentUID:     uint32(run.ID),
		}
		if err := db.FinishSyncRun(ctx, user.ID, run.ID, status, progress, ""); err != nil {
			t.Fatal(err)
		}
		return run
	}

	oldInboxNoop := createRun("INBOX", 0, "ok")
	storedInbox := createRun("INBOX", 3, "ok")
	newerInboxNoop := createRun("INBOX", 0, "ok")
	latestInboxNoop := createRun("INBOX", 0, "ok")
	archiveNoop := createRun("Archive", 0, "ok")
	failedNoop := createRun("INBOX", 0, "failed")

	runs, err := db.ListSyncRunsForUser(ctx, user.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, run := range runs {
		ids[run.ID] = true
	}
	for _, run := range []SyncRun{storedInbox, latestInboxNoop, archiveNoop, failedNoop} {
		if !ids[run.ID] {
			t.Fatalf("expected run %d in recent list; got %+v", run.ID, runs)
		}
	}
	for _, run := range []SyncRun{oldInboxNoop, newerInboxNoop} {
		if ids[run.ID] {
			t.Fatalf("redundant no-op run %d was not collapsed; got %+v", run.ID, runs)
		}
	}
}

func TestMarkRunningSyncRunsInterruptedSurvivesLateFinish(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, account, _, _ := testMailbox(t, ctx, db)

	run, err := db.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	progress := SyncProgress{
		MessagesSeen:   12,
		MessagesStored: 7,
		MessagesTotal:  30,
		MailboxesTotal: 2,
		CurrentMailbox: "Archive",
		CurrentUID:     991,
	}
	if err := db.UpdateSyncRunProgress(ctx, user.ID, run.ID, progress); err != nil {
		t.Fatal(err)
	}

	n, err := db.MarkRunningSyncRunsInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("interrupted rows = %d", n)
	}
	interrupted, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Status != "interrupted" {
		t.Fatalf("status = %q", interrupted.Status)
	}
	if interrupted.FinishedAt.IsZero() {
		t.Fatalf("finished_at was not set: %+v", interrupted)
	}
	if interrupted.Error == "" {
		t.Fatalf("expected interruption error text")
	}

	if err := db.FinishSyncRun(ctx, user.ID, run.ID, "ok", SyncProgress{MessagesSeen: 99, MessagesStored: 99, MailboxesDone: 2, MailboxesTotal: 2}, ""); err != nil {
		t.Fatal(err)
	}
	afterLateFinish, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLateFinish.Status != "interrupted" {
		t.Fatalf("late finish overwrote status: %+v", afterLateFinish)
	}
	if afterLateFinish.MessagesSeen != progress.MessagesSeen || afterLateFinish.MessagesStored != progress.MessagesStored {
		t.Fatalf("late finish overwrote progress: %+v", afterLateFinish)
	}
	if afterLateFinish.Error != interrupted.Error {
		t.Fatalf("late finish overwrote error: before=%q after=%q", interrupted.Error, afterLateFinish.Error)
	}
}

func TestUpdateUserDisplayPreferencesPersistsTheme(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "theme@example.test", "Theme", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := db.UpdateUserDisplayPreferences(ctx, user.ID, "en-GB", "dmy", "matrix")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DateLocale != "en-GB" || updated.DateFormat != "dmy" || updated.Theme != "matrix" {
		t.Fatalf("updated preferences = %+v", updated)
	}

	if _, err := db.CreateSession(ctx, user.ID, "theme-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, sessionUser, err := db.GetSessionUser(ctx, "theme-token")
	if err != nil {
		t.Fatal(err)
	}
	if sessionUser.Theme != "matrix" {
		t.Fatalf("session user theme = %q", sessionUser.Theme)
	}

	updated, err = db.UpdateUserDisplayPreferences(ctx, user.ID, "", "locale", "classic-dark")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DateFormat != "locale" || updated.Theme != "classic_dark" {
		t.Fatalf("classic dark preferences = %+v", updated)
	}

	updated, err = db.UpdateUserDisplayPreferences(ctx, user.ID, "", "bogus", "neon")
	if err != nil {
		t.Fatal(err)
	}
	if updated.DateFormat != "mdy" || updated.Theme != "classic" {
		t.Fatalf("normalized preferences = %+v", updated)
	}
}

func TestOnboardingMailboxDefaultsDiscoverAllButAutoSyncInboxOnly(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{UserID: user.ID, Email: "owner@example.test", Host: "imap.example.test", Port: 993, Username: "owner@example.test", EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if account.Mailbox != DefaultMailboxPattern {
		t.Fatalf("account.Mailbox = %q, want %q", account.Mailbox, DefaultMailboxPattern)
	}
	inbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archives.2024")
	if err != nil {
		t.Fatal(err)
	}
	child, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX.spam")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.SyncMode != "auto" || inbox.Role != "inbox" {
		t.Fatalf("inbox defaults = mode %q role %q, want auto/inbox", inbox.SyncMode, inbox.Role)
	}
	if archive.SyncMode != "manual" {
		t.Fatalf("archive sync mode = %q, want manual", archive.SyncMode)
	}
	if child.SyncMode != "manual" {
		t.Fatalf("inbox child sync mode = %q, want manual", child.SyncMode)
	}
}

func TestUpdateMailboxSettingsRejectsInheritForTopLevelFolder(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{UserID: user.ID, Email: "owner@example.test", Host: "imap.example.test", Port: 993, Username: "owner@example.test", EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	err = db.UpdateMailboxSettings(ctx, user.ID, archive.ID, MailboxSettings{
		SyncMode:        "inherit",
		Role:            "",
		Icon:            "folder",
		ShowInSidebar:   true,
		ShowInAllMail:   true,
		IncludeInSearch: true,
	})
	if !errors.Is(err, ErrInvalidMailboxSettings) {
		t.Fatalf("error = %v, want ErrInvalidMailboxSettings", err)
	}
	child, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archive.2024")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSettings(ctx, user.ID, child.ID, MailboxSettings{
		SyncMode:        "inherit",
		Role:            "",
		Icon:            "folder",
		ShowInSidebar:   true,
		ShowInAllMail:   true,
		IncludeInSearch: true,
	}); err != nil {
		t.Fatalf("child inherit update: %v", err)
	}
}

func TestUpdateMailboxSettingsRejectsDuplicateSpecialRole(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{UserID: user.ID, Email: "owner@example.test", Host: "imap.example.test", Port: 993, Username: "owner@example.test", EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	err = db.UpdateMailboxSettings(ctx, user.ID, archive.ID, MailboxSettings{
		SyncMode:        "manual",
		Role:            "inbox",
		Icon:            "inbox",
		ShowInSidebar:   true,
		ShowInAllMail:   true,
		IncludeInSearch: true,
	})
	if !errors.Is(err, ErrDuplicateMailboxRole) {
		t.Fatalf("error = %v, want ErrDuplicateMailboxRole", err)
	}
	currentInbox, err := db.GetMailboxForUser(ctx, user.ID, inbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentInbox.Role != "inbox" {
		t.Fatalf("inbox role = %q, want inbox", currentInbox.Role)
	}
}

func TestEnsureMeContactForEmailSeedsIdentityAndDefaultSMTP(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	contact, err := db.EnsureMeContactForEmail(ctx, user.ID, user.Email, user.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !contact.IsMe || !contact.IsPrimary {
		t.Fatalf("onboarding contact flags = me %t primary %t, want true/true", contact.IsMe, contact.IsPrimary)
	}
	if len(contact.Emails) != 1 || !contact.Emails[0].IsPrimary {
		t.Fatalf("onboarding contact emails = %+v, want one primary email", contact.Emails)
	}
	identities, err := db.ListMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].Email != user.Email || identities[0].SMTPAccountID != 0 || !identities[0].IsPrimary {
		t.Fatalf("identities before smtp = %+v", identities)
	}
	smtp, err := db.CreateSMTPAccount(ctx, SMTPAccount{UserID: user.ID, Label: "Owner", Host: "smtp.example.test", Port: 587, Username: user.Email, EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	identities, err = db.ListMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].SMTPAccountID != smtp.ID {
		t.Fatalf("identities after smtp = %+v, want smtp id %d", identities, smtp.ID)
	}
}

func TestMailAccountsAndIdentitiesStayScopedByUser(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "mailmirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "multi@example.test", "Multi", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "other-multi@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.UpsertMailAccount(ctx, MailAccount{UserID: user.ID, Email: "first@example.test", Host: "imap.first.test", Port: 993, Username: "first", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateMailAccount(ctx, MailAccount{UserID: user.ID, Email: "second@example.test", Host: "imap.second.test", Port: 993, Username: "second", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.UpsertMailAccount(ctx, MailAccount{UserID: other.ID, Email: "other@example.test", Host: "imap.other.test", Port: 993, Username: "other", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := db.ListMailAccountsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != first.ID || accounts[1].ID != second.ID {
		t.Fatalf("accounts = %+v", accounts)
	}
	if _, err := db.GetMailAccountForUser(ctx, user.ID, otherAccount.ID); !IsNotFound(err) {
		t.Fatalf("cross-user account lookup err = %v, want not found", err)
	}
	userSMTP, err := db.CreateSMTPAccount(ctx, SMTPAccount{UserID: user.ID, Label: "User SMTP", Host: "smtp.user.test", Port: 587, Username: "multi", EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	otherSMTP, err := db.CreateSMTPAccount(ctx, SMTPAccount{UserID: other.ID, Label: "Other SMTP", Host: "smtp.other.test", Port: 587, Username: "other", EncryptedPassword: "secret", UseTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, user.ID, Contact{DisplayName: "Multi User", IsMe: true, IsPrimary: true, Emails: []ContactEmail{{Email: "multi@example.test", IsPrimary: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateContact(ctx, other.ID, Contact{DisplayName: "Other User", IsMe: true, IsPrimary: true, Emails: []ContactEmail{{Email: "other-multi@example.test", IsPrimary: true}}}); err != nil {
		t.Fatal(err)
	}
	identities, err := db.ListMailIdentitiesForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentities, err := db.ListMailIdentitiesForUser(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || len(otherIdentities) != 1 {
		t.Fatalf("identities=%+v other=%+v", identities, otherIdentities)
	}
	if _, err := db.UpdateMailIdentityForUser(ctx, user.ID, MailIdentity{ID: otherIdentities[0].ID, SMTPAccountID: userSMTP.ID, DisplayName: "bad"}); !IsNotFound(err) {
		t.Fatalf("cross-user identity lookup err = %v, want not found", err)
	}
	if _, err := db.UpdateMailIdentityForUser(ctx, user.ID, MailIdentity{ID: identities[0].ID, SMTPAccountID: otherSMTP.ID, DisplayName: "bad"}); !IsNotFound(err) {
		t.Fatalf("cross-user SMTP link err = %v, want not found", err)
	}
}

func testMailbox(t *testing.T, ctx context.Context, db *Store) (User, MailAccount, Mailbox, BlobRecord) {
	t.Helper()
	user, err := db.CreateUser(ctx, "mail@example.test", "Mail", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, MailAccount{UserID: user.ID, Email: "mail@example.test", Host: "imap.example.test", Port: 993, Username: "mail", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, BlobRecord{UserID: user.ID, Kind: "message", Path: "users/1/blobs/accounts/1/mailboxes/INBOX/uid-1.eml", SHA256: "deadbeef", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	return user, account, mailbox, blob
}
