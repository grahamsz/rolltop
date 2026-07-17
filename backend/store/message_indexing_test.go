package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMarkSearchVisibleMessagesPendingIndexIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUser(ctx, "search-reset@example.test", "Search Reset", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "search-reset-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerVisible := createIndexedMessageForResetTest(t, ctx, db, owner, "INBOX", true, 1)
	ownerHidden := createIndexedMessageForResetTest(t, ctx, db, owner, "Archive", false, 2)
	otherVisible := createIndexedMessageForResetTest(t, ctx, db, other, "INBOX", true, 1)

	marked, err := db.MarkSearchVisibleMessagesPendingIndex(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked != 1 {
		t.Fatalf("marked = %d, want 1", marked)
	}
	assertResetIndexState(t, ctx, db, owner.ID, ownerVisible.ID, true, ownerVisible)
	assertResetIndexState(t, ctx, db, owner.ID, ownerHidden.ID, false, ownerHidden)
	assertResetIndexState(t, ctx, db, other.ID, otherVisible.ID, false, otherVisible)
}

func TestMarkSearchVisibleMessagesPendingIndexUsesFullThenRestoresNormal(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "durable-search-reset@example.test", "Durable Search Reset", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createIndexedMessageForResetTest(t, ctx, db, owner, "INBOX", true, 1)
	db.db.SetMaxOpenConns(1)

	events := make([]string, 0, 3)
	marked, err := markSearchVisibleMessagesPendingIndexWithSynchronous(ctx, db.db, owner.ID,
		func(ctx context.Context, conn *sql.Conn, mode string) error {
			events = append(events, mode)
			return setSQLiteSynchronous(ctx, conn, mode)
		}, func(ctx context.Context, conn *sql.Conn) error {
			events = append(events, "CHECKPOINT")
			return fullSQLiteWALCheckpoint(ctx, conn)
		})
	if err != nil {
		t.Fatal(err)
	}
	if marked != 1 {
		t.Fatalf("marked = %d, want 1", marked)
	}
	if !slices.Equal(events, []string{"FULL", "CHECKPOINT", "NORMAL"}) {
		t.Fatalf("durability events = %v, want [FULL CHECKPOINT NORMAL]", events)
	}
	// The second update targets a row that is already pending. The checkpoint
	// must still run because SQLite is free to avoid writing an unchanged value.
	events = events[:0]
	marked, err = markSearchVisibleMessagesPendingIndexWithSynchronous(ctx, db.db, owner.ID,
		func(ctx context.Context, conn *sql.Conn, mode string) error {
			events = append(events, mode)
			return setSQLiteSynchronous(ctx, conn, mode)
		}, func(ctx context.Context, conn *sql.Conn) error {
			events = append(events, "CHECKPOINT")
			return fullSQLiteWALCheckpoint(ctx, conn)
		})
	if err != nil || marked != 1 {
		t.Fatalf("already-pending durable mark = %d, %v; want 1, nil", marked, err)
	}
	if !slices.Equal(events, []string{"FULL", "CHECKPOINT", "NORMAL"}) {
		t.Fatalf("already-pending durability events = %v, want [FULL CHECKPOINT NORMAL]", events)
	}
	var synchronous int
	if err := db.db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Fatalf("SQLite synchronous mode = %d, want NORMAL (1)", synchronous)
	}
	assertResetIndexState(t, ctx, db, owner.ID, message.ID, true, message)
}

func TestDurableSearchPendingWriteDiscardsConnectionWhenNormalRestoreFails(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "failed-search-restore@example.test", "Failed Search Restore", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createIndexedMessageForResetTest(t, ctx, db, owner, "INBOX", true, 1)
	db.db.SetMaxOpenConns(1)
	restoreErr := errors.New("restore failed")

	marked, err := markSearchVisibleMessagesPendingIndexWithSynchronous(ctx, db.db, owner.ID,
		func(ctx context.Context, conn *sql.Conn, mode string) error {
			if mode == "NORMAL" {
				return restoreErr
			}
			return setSQLiteSynchronous(ctx, conn, mode)
		}, fullSQLiteWALCheckpoint)
	if marked != 1 || !errors.Is(err, restoreErr) {
		t.Fatalf("marked=%d error=%v, want 1 and restore failure", marked, err)
	}
	var synchronous int
	if err := db.db.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Fatalf("replacement SQLite connection synchronous mode = %d, want NORMAL (1)", synchronous)
	}
	assertResetIndexState(t, ctx, db, owner.ID, message.ID, true, message)
}

func TestFullSQLiteWALCheckpointRejectsNonWALSentinel(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	err = fullSQLiteWALCheckpoint(context.Background(), conn)
	if err == nil || !strings.Contains(err.Error(), "WAL checkpoint unavailable") {
		t.Fatalf("non-WAL checkpoint error = %v, want unavailable sentinel rejection", err)
	}
}

func TestMarkMessageAttachmentIndexPendingIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUser(ctx, "message-index-pending@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "message-index-pending-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	message := createIndexedMessageForResetTest(t, ctx, db, owner, "INBOX", true, 1)

	if err := db.MarkMessageAttachmentIndexPending(ctx, other.ID, message.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant pending error=%v, want not found", err)
	}
	unchanged, err := db.GetMessageForUser(ctx, owner.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.AttachmentIndexedAt.IsZero() {
		t.Fatal("cross-tenant update reset the owner's index marker")
	}
	if err := db.MarkMessageAttachmentIndexPending(ctx, owner.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	pending, err := db.GetMessageForUser(ctx, owner.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pending.AttachmentIndexedAt.IsZero() {
		t.Fatal("owner message did not become pending")
	}
}

func TestListMessagesNeedingAttachmentIndexAfterWrapsWithinTenant(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUser(ctx, "search-cursor@example.test", "Search Cursor", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(ctx, "search-cursor-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	ownerMessages := make([]MessageRecord, 0, 4)
	for i, name := range []string{"Cursor A", "Cursor B", "Cursor C", "Cursor D"} {
		ownerMessages = append(ownerMessages, createIndexedMessageForResetTest(t, ctx, db, owner, name, true, uint32(i+1)))
	}
	otherMessage := createIndexedMessageForResetTest(t, ctx, db, other, "Other Cursor", true, 1)
	if _, err := db.MarkSearchVisibleMessagesPendingIndex(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}

	page, wrapped, err := db.ListMessagesNeedingAttachmentIndexAfter(ctx, owner.ID, ownerMessages[1].ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !wrapped {
		t.Fatal("cursor page did not report wrapping")
	}
	want := []int64{ownerMessages[2].ID, ownerMessages[3].ID, ownerMessages[0].ID}
	if len(page) != len(want) {
		t.Fatalf("cursor page length = %d, want %d", len(page), len(want))
	}
	for i := range want {
		if page[i].ID != want[i] || page[i].UserID != owner.ID {
			t.Fatalf("cursor page[%d] = message %d user %d, want message %d user %d", i, page[i].ID, page[i].UserID, want[i], owner.ID)
		}
		if page[i].ID == otherMessage.ID && page[i].UserID == other.ID {
			t.Fatal("cursor page crossed tenant scope")
		}
	}

	otherPending, err := db.ListMessagesNeedingAttachmentIndex(ctx, other.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherPending) != 0 {
		t.Fatalf("other tenant pending messages = %d, want 0", len(otherPending))
	}
}

func createIndexedMessageForResetTest(t *testing.T, ctx context.Context, db *Store, user User, mailboxName string, include bool, uid uint32) MessageRecord {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, mailboxName)
	if err != nil {
		t.Fatal(err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE mailboxes SET include_in_search = ? WHERE user_id = ? AND id = ?`, boolInt(include), user.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}
	blob, err := db.CreateBlob(ctx, BlobRecord{UserID: user.ID, Kind: "message", Path: mailboxName + ".eml", SHA256: mailboxName, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<" + mailboxName + "@example.test>", Subject: "Preserve " + mailboxName,
		FromAddr: "sender@example.test", ToAddr: user.Email, Date: time.Now(), InternalDate: time.Now(),
		UID: uid, Size: 10, BlobPath: blob.Path, BodyText: "preserved body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageAttachmentIndexed(ctx, user.ID, message.ID, false); err != nil {
		t.Fatal(err)
	}
	message, err = db.GetMessageForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func assertResetIndexState(t *testing.T, ctx context.Context, db *Store, userID, messageID int64, pending bool, before MessageRecord) {
	t.Helper()
	after, err := db.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AttachmentIndexedAt.IsZero() != pending {
		t.Fatalf("message %d pending = %t, want %t", messageID, after.AttachmentIndexedAt.IsZero(), pending)
	}
	if after.Subject != before.Subject || after.BodyText != before.BodyText || after.BlobID != before.BlobID || after.BlobPath != before.BlobPath || after.UID != before.UID {
		t.Fatalf("message content changed: before=%+v after=%+v", before, after)
	}
}
