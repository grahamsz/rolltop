package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type journaledSnoozeFixture struct {
	user         User
	account      MailAccount
	mailbox      Mailbox
	source       MessageRecord
	raw          []byte
	rawSHA       string
	internalDate time.Time
	uidValidity  uint32
}

func TestNewMailArrivalCancelsJournaledSnoozeAcrossMailboxAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const threadKey = "shared-rebuild-snooze-thread"
	owner := createJournaledSnoozeFixture(t, db, "owner", threadKey, 9101)
	other := createJournaledSnoozeFixture(t, db, "other", threadKey, 9201)

	assertJournaledSnoozeState(t, db, owner.user.ID, threadKey, 1, 1)
	assertJournaledSnoozeState(t, db, other.user.ID, threadKey, 1, 1)

	inbox, err := db.GetOrCreateMailbox(ctx, owner.user.ID, owner.account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	const inboxUIDValidity = 9301
	if err := db.UpdateMailboxRemoteStatus(ctx, owner.user.ID, inbox.ID, 1, 0, 52, inboxUIDValidity); err != nil {
		t.Fatal(err)
	}
	arrivalRaw := []byte("Message-ID: <arrival@example.test>\r\nSubject: New reply\r\n\r\nreply\r\n")
	arrivalSum := sha256.Sum256(arrivalRaw)
	arrivalPath := fmt.Sprintf("users/%d/blobs/arrival.eml", owner.user.ID)
	arrivalBlob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: owner.user.ID, Kind: "message", Path: arrivalPath,
		SHA256: hex.EncodeToString(arrivalSum[:]), Size: int64(len(arrivalRaw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	arrival, err := db.CreateMessage(ctx, CreateMessage{
		UserID: owner.user.ID, AccountID: owner.account.ID, MailboxID: inbox.ID, BlobID: arrivalBlob.ID,
		MessageIDHeader: "<arrival@example.test>", ThreadKey: threadKey, Subject: "New reply",
		FromAddr: "Sender <sender@example.test>", Date: owner.internalDate.Add(time.Hour),
		InternalDate: owner.internalDate.Add(time.Hour), UID: 51, UIDValidity: inboxUIDValidity,
		Size: int64(len(arrivalRaw)), BlobPath: arrivalPath, BodyText: "reply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := db.RecordNewMailEvent(ctx, owner.user.ID, arrival); err != nil || !created {
		t.Fatalf("record new-mail arrival created=%v err=%v", created, err)
	}

	assertJournaledSnoozeState(t, db, owner.user.ID, threadKey, 0, 0)
	assertJournaledSnoozeState(t, db, other.user.ID, threadKey, 1, 1)
	if cancelled, err := db.CancelSnoozeForNewMessage(ctx, owner.user.ID, arrival); err != nil || cancelled {
		t.Fatalf("repeat cancellation cancelled=%v err=%v, want false/nil", cancelled, err)
	}
	if _, created, err := db.RecordNewMailEvent(ctx, owner.user.ID, arrival); err != nil || created {
		t.Fatalf("replayed arrival created=%v err=%v, want false/nil", created, err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertJournaledSnoozeState(t, db, owner.user.ID, threadKey, 0, 0)
	assertJournaledSnoozeState(t, db, other.user.ID, threadKey, 1, 1)

	replacementPath := fmt.Sprintf("users/%d/blobs/historical-refetched.eml", owner.user.ID)
	replacementBlob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: owner.user.ID, Kind: "message", Path: replacementPath,
		SHA256: owner.rawSHA, Size: int64(len(owner.raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := db.CreateMessage(ctx, CreateMessage{
		UserID: owner.user.ID, AccountID: owner.account.ID, MailboxID: owner.mailbox.ID,
		BlobID: replacementBlob.ID, MessageIDHeader: owner.source.MessageIDHeader,
		ThreadKey: threadKey, Subject: owner.source.Subject, FromAddr: owner.source.FromAddr,
		Date: owner.internalDate, InternalDate: owner.internalDate, UID: owner.source.UID,
		UIDValidity: int64(owner.uidValidity), Size: int64(len(owner.raw)),
		BlobPath: replacementPath, BodyText: "historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MessageSnoozeForUser(ctx, owner.user.ID, replacement.ID); !IsNotFound(err) {
		t.Fatalf("restored historical message snooze err=%v, want not found", err)
	}
	var ownerReminders int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM snooze_reminder_events
		WHERE user_id = ?`, owner.user.ID).Scan(&ownerReminders); err != nil {
		t.Fatal(err)
	}
	if ownerReminders != 0 {
		t.Fatalf("restored historical message resurrected %d reminder events", ownerReminders)
	}
	assertJournaledSnoozeState(t, db, other.user.ID, threadKey, 1, 1)
}

func createJournaledSnoozeFixture(t *testing.T, db *Store, suffix, threadKey string, uidValidity uint32) journaledSnoozeFixture {
	t.Helper()
	ctx := context.Background()
	user, err := db.CreateUser(ctx, fmt.Sprintf("journal-snooze-%s@example.test", suffix), suffix, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: suffix, EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 12, uidValidity); err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf("Message-ID: <historical-%s@example.test>\r\nSubject: Historical\r\n\r\nhistorical\r\n", suffix))
	sum := sha256.Sum256(raw)
	rawSHA := hex.EncodeToString(sum[:])
	blobPath := fmt.Sprintf("users/%d/blobs/historical-%s.eml", user.ID, suffix)
	blob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID, Kind: "message", Path: blobPath, SHA256: rawSHA, Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	internalDate := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: fmt.Sprintf("<historical-%s@example.test>", suffix), ThreadKey: threadKey,
		Subject: "Historical", FromAddr: "Sender <sender@example.test>", Date: internalDate,
		InternalDate: internalDate, UID: 11, UIDValidity: int64(uidValidity), Size: int64(len(raw)),
		BlobPath: blobPath, BodyText: "historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	snoozedUntil := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if _, err := db.SnoozeMessage(ctx, user.ID, message.ID, snoozedUntil); err != nil {
		t.Fatal(err)
	}
	reminders, err := db.RecordDueSnoozeReminderEvents(ctx, user.ID, snoozedUntil.Add(time.Hour), 10)
	if err != nil || len(reminders) != 1 {
		t.Fatalf("record snooze reminder events=%+v err=%v", reminders, err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET uid_validity = 0
		WHERE user_id = ? AND id = ?`, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	stale, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, account.ID, mailbox.ID, uidValidity, 12)
	if err != nil || !reset || len(stale) != 1 {
		t.Fatalf("generation reset stale=%d reset=%v err=%v", len(stale), reset, err)
	}
	return journaledSnoozeFixture{
		user: user, account: account, mailbox: mailbox, source: message, raw: raw,
		rawSHA: rawSHA, internalDate: internalDate, uidValidity: uidValidity,
	}
}

func assertJournaledSnoozeState(t *testing.T, db *Store, userID int64, threadKey string, wantSnooze, wantReminders int) {
	t.Helper()
	ctx := context.Background()
	var snoozes int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ? AND snooze_thread_key = ? AND has_snooze <> 0`, userID, threadKey).Scan(&snoozes); err != nil {
		t.Fatal(err)
	}
	var reminders int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*)
		FROM mailbox_generation_rebuild_snooze_events event
		JOIN mailbox_generation_rebuild_messages message
			ON message.user_id = event.user_id AND message.id = event.rebuild_message_id
		WHERE event.user_id = ? AND message.snooze_thread_key = ?`, userID, threadKey).Scan(&reminders); err != nil {
		t.Fatal(err)
	}
	if snoozes != wantSnooze || reminders != wantReminders {
		t.Fatalf("journal snoozes/reminders=%d/%d, want %d/%d", snoozes, reminders, wantSnooze, wantReminders)
	}
}
