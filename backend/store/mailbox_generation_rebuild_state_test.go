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

func TestLegacyUIDValidityRebuildPreservesDurableMessageStateAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "generation-state@example.test", "Generation State", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "generation-state", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 42, 9001); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <generation-state@example.test>\r\nFrom: Sender <sender@example.test>\r\nSubject: Preserved state\r\n\r\nbody\r\n")
	sum := sha256.Sum256(raw)
	rawSHA := hex.EncodeToString(sum[:])
	blob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID, Kind: "message", Path: "users/generation-state.eml", SHA256: rawSHA, Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	internalDate := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: "<generation-state@example.test>", Subject: "Preserved state",
		FromAddr: "Sender <sender@example.test>", Date: internalDate, InternalDate: internalDate,
		UID: 41, UIDValidity: 9001, Size: int64(len(raw)), BlobPath: blob.Path,
		BodyText: "body", IsRead: false, IsStarred: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	newMailEvent, created, err := db.RecordNewMailEvent(ctx, user.ID, message)
	if err != nil || !created {
		t.Fatalf("record new-mail event created=%v err=%v", created, err)
	}
	snoozedUntil := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	snooze, err := db.SnoozeMessage(ctx, user.ID, message.ID, snoozedUntil)
	if err != nil {
		t.Fatal(err)
	}
	reminders, err := db.RecordDueSnoozeReminderEvents(ctx, user.ID, snoozedUntil.Add(time.Hour), 10)
	if err != nil || len(reminders) != 1 {
		t.Fatalf("record reminders=%+v err=%v", reminders, err)
	}
	reminder := reminders[0]
	snooze, err = db.MessageSnoozeForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.DB().ExecContext(ctx, `INSERT INTO plugin_one_click_unsubscribe_sends
		(user_id, message_id, sender, unsubscribe_url, sent_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, user.ID, message.ID, "sender@example.test",
		"https://example.test/unsubscribe", internalDate.Unix(), internalDate.Unix())
	if err != nil {
		t.Fatal(err)
	}
	oneClickID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET
		is_read = 1, read_sync_pending = 1, is_starred = 0, star_sync_pending = 1
		WHERE user_id = ? AND id = ?`, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	// This is the state immediately after schema 022 is installed: the mailbox
	// has a cached generation, but pre-022 message rows remain intentionally unproven.
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET uid_validity = 0
		WHERE user_id = ? AND id = ?`, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	stale, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, account.ID, mailbox.ID, 9001, 42)
	if err != nil || !reset || len(stale) != 1 {
		t.Fatalf("generation reset stale=%d reset=%v err=%v", len(stale), reset, err)
	}
	var journalRows, liveSnoozes int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, user.ID, account.ID, mailbox.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_snoozes WHERE user_id = ?`, user.ID).Scan(&liveSnoozes); err != nil {
		t.Fatal(err)
	}
	if journalRows != 1 || liveSnoozes != 0 {
		t.Fatalf("after reset journal=%d live_snoozes=%d, want 1/0", journalRows, liveSnoozes)
	}
	deleted, err := db.DeleteBlobIfUnreferencedForUser(ctx, user.ID, blob.ID)
	if err != nil || !deleted {
		t.Fatalf("delete stale blob deleted=%v err=%v", deleted, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	replacementBlob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: user.ID, Kind: "message", Path: "users/generation-state-refetched.eml", SHA256: rawSHA, Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: replacementBlob.ID,
		MessageIDHeader: "<generation-state@example.test>", Subject: "Preserved state",
		FromAddr: "Sender <sender@example.test>", Date: internalDate, InternalDate: internalDate,
		UID: 41, UIDValidity: 9001, Size: int64(len(raw)), BlobPath: replacementBlob.Path,
		BodyText: "body", IsRead: false, IsStarred: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == message.ID {
		t.Fatal("refetch unexpectedly reused the deleted message id")
	}
	replacement, err = db.GetMessageForUser(ctx, user.ID, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !replacement.IsRead || !replacement.ReadSyncPending || replacement.IsStarred || !replacement.StarSyncPending {
		t.Fatalf("restored flags read=%v read_pending=%v starred=%v star_pending=%v",
			replacement.IsRead, replacement.ReadSyncPending, replacement.IsStarred, replacement.StarSyncPending)
	}
	restoredSnooze, err := db.MessageSnoozeForUser(ctx, user.ID, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredSnooze.ID != snooze.ID || restoredSnooze.MessageID != replacement.ID ||
		restoredSnooze.Generation != snooze.Generation || !restoredSnooze.SnoozedUntil.Equal(snooze.SnoozedUntil) ||
		restoredSnooze.RemindedAt != snooze.RemindedAt {
		t.Fatalf("restored snooze=%+v want original id/generation/times on message %d", restoredSnooze, replacement.ID)
	}
	var reminderMessageID int64
	if err := db.DB().QueryRowContext(ctx, `SELECT message_id FROM snooze_reminder_events
		WHERE user_id = ? AND id = ?`, user.ID, reminder.ID).Scan(&reminderMessageID); err != nil {
		t.Fatal(err)
	}
	if reminderMessageID != replacement.ID {
		t.Fatalf("restored reminder message_id=%d, want %d", reminderMessageID, replacement.ID)
	}
	restoredEvents, eventCount, eventCursor, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil || eventCount != 1 || eventCursor != newMailEvent.ID || len(restoredEvents) != 1 ||
		restoredEvents[0].ID != newMailEvent.ID || restoredEvents[0].MessageID != replacement.ID {
		t.Fatalf("restored new-mail events=%+v count=%d cursor=%d err=%v", restoredEvents, eventCount, eventCursor, err)
	}
	var sendMessageID int64
	if err := db.DB().QueryRowContext(ctx, `SELECT message_id FROM plugin_one_click_unsubscribe_sends
		WHERE user_id = ? AND id = ?`, user.ID, oneClickID).Scan(&sendMessageID); err != nil {
		t.Fatal(err)
	}
	if sendMessageID != replacement.ID {
		t.Fatalf("restored one-click send message_id=%d, want %d", sendMessageID, replacement.ID)
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, account.ID, mailbox.ID, 9001); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ?`, user.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if journalRows != 0 {
		t.Fatalf("completed rebuild retained %d journal rows", journalRows)
	}
}

func TestMailboxGenerationRebuildContinuesAfterPartialRefetchRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "partial-generation@example.test", "Partial Generation", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "partial-generation", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 2, 0, 3, 7001); err != nil {
		t.Fatal(err)
	}
	internalDate := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	snoozeBase := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	rawMessages := [][]byte{
		[]byte("Message-ID: <partial-1@example.test>\r\nSubject: Partial one\r\n\r\none\r\n"),
		[]byte("Message-ID: <partial-2@example.test>\r\nSubject: Partial two\r\n\r\ntwo\r\n"),
	}
	originalSnoozeIDs := make([]int64, len(rawMessages))
	for i, raw := range rawMessages {
		sum := sha256.Sum256(raw)
		blob, err := db.CreateBlob(ctx, BlobRecord{
			UserID: user.ID, Kind: "message", Path: fmt.Sprintf("users/partial-original-%d.eml", i),
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)),
		})
		if err != nil {
			t.Fatal(err)
		}
		message, err := db.CreateMessage(ctx, CreateMessage{
			UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
			MessageIDHeader: fmt.Sprintf("<partial-%d@example.test>", i+1), Subject: fmt.Sprintf("Partial %d", i+1),
			Date: internalDate, InternalDate: internalDate, UID: uint32(i + 1), UIDValidity: 7001,
			Size: int64(len(raw)), BlobPath: blob.Path, BodyText: fmt.Sprintf("%d", i+1),
		})
		if err != nil {
			t.Fatal(err)
		}
		snooze, err := db.SnoozeMessage(ctx, user.ID, message.ID, snoozeBase.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		originalSnoozeIDs[i] = snooze.ID
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET uid_validity = 0
		WHERE user_id = ? AND mailbox_id = ?`, user.ID, mailbox.ID); err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteGeneration(ctx, user.ID, account.ID, mailbox.ID, 7001, 3); err != nil || !reset {
		t.Fatalf("generation reset reset=%v err=%v", reset, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	refetchedIDs := make([]int64, len(rawMessages))
	refetch := func(db *Store, i int) {
		t.Helper()
		raw := rawMessages[i]
		sum := sha256.Sum256(raw)
		blob, err := db.CreateBlob(ctx, BlobRecord{
			UserID: user.ID, Kind: "message", Path: fmt.Sprintf("users/partial-refetched-%d.eml", i),
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)),
		})
		if err != nil {
			t.Fatal(err)
		}
		message, err := db.CreateMessage(ctx, CreateMessage{
			UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
			MessageIDHeader: fmt.Sprintf("<partial-%d@example.test>", i+1), Subject: fmt.Sprintf("Partial %d", i+1),
			Date: internalDate, InternalDate: internalDate, UID: uint32(i + 1), UIDValidity: 7001,
			Size: int64(len(raw)), BlobPath: blob.Path, BodyText: fmt.Sprintf("%d", i+1),
		})
		if err != nil {
			t.Fatal(err)
		}
		refetchedIDs[i] = message.ID
		snooze, err := db.MessageSnoozeForUser(ctx, user.ID, message.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snooze.ID != originalSnoozeIDs[i] {
			t.Fatalf("refetched message %d snooze id=%d, want %d", i, snooze.ID, originalSnoozeIDs[i])
		}
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	refetch(db, 0)
	var journalRows int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, user.ID, account.ID, mailbox.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if journalRows != 1 {
		t.Fatalf("partial refetch retained %d journal rows, want 1", journalRows)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	refetch(db, 1)
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, refetchedIDs[0]); err != nil {
		t.Fatalf("first restored snooze did not survive restart: %v", err)
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, account.ID, mailbox.ID, 7001); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_messages
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ?`, user.ID, account.ID, mailbox.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if journalRows != 0 {
		t.Fatalf("continued refetch retained %d journal rows", journalRows)
	}
}
