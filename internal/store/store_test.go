package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		Path:   "blobs/users/1/accounts/1/mailboxes/INBOX/uid-3449-deadbeef.eml",
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

func TestBackfillThreadHeadersFromBlobsRepairsLegacyRows(t *testing.T) {
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
		Subject:         "Legacy Thread",
		Date:            time.Now().Add(-time.Hour),
		UID:             1,
		BlobPath:        blob.Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	replyPath := "blobs/users/1/accounts/1/mailboxes/INBOX/uid-2.eml"
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
		Subject:         "Re: Legacy Thread",
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
	if err := os.WriteFile(filepath.Join(dataDir, replyPath), []byte("From sender@example.test Sat Jan 01 00:00:00 2026\r\nMessage-ID: <reply@example.test>\r\nIn-Reply-To: <root@example.test>\r\nReferences: <root@example.test>\r\nSubject: Re: Legacy Thread\r\n\r\nbody is ignored\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET in_reply_to = '', references_header = '', thread_key = ?, thread_headers_checked_at = 0 WHERE id = ?`, ThreadKey("<reply@example.test>", "", "", "Re: Legacy Thread"), reply.ID); err != nil {
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
	otherBlob, err := db.CreateBlob(ctx, BlobRecord{UserID: other.ID, Kind: "message", Path: "blobs/users/2/accounts/1/mailboxes/INBOX/uid-1.eml", SHA256: "bead", Size: 1})
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
	blob, err := db.CreateBlob(ctx, BlobRecord{UserID: user.ID, Kind: "message", Path: "blobs/users/1/accounts/1/mailboxes/INBOX/uid-1.eml", SHA256: "deadbeef", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	return user, account, mailbox, blob
}
