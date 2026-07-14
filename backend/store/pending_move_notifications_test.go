package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPendingMoveNotificationIsScopedByTenantAccountMailboxAndRawHash(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner := createPendingMoveTestUser(t, ctx, db, "move-owner@example.test")
	other := createPendingMoveTestUser(t, ctx, db, "move-other@example.test")
	ownerAccount := createPendingMoveTestAccount(t, ctx, db, owner, "primary")
	ownerOtherAccount := createPendingMoveTestAccount(t, ctx, db, owner, "secondary")
	otherAccount := createPendingMoveTestAccount(t, ctx, db, other, "primary")
	ownerSource := createPendingMoveTestMailbox(t, ctx, db, owner, ownerAccount, "Spam")
	ownerInbox := createPendingMoveTestMailbox(t, ctx, db, owner, ownerAccount, "INBOX")
	ownerArchive := createPendingMoveTestMailbox(t, ctx, db, owner, ownerAccount, "Archive")
	ownerOtherInbox := createPendingMoveTestMailbox(t, ctx, db, owner, ownerOtherAccount, "INBOX")
	otherInbox := createPendingMoveTestMailbox(t, ctx, db, other, otherAccount, "INBOX")

	hash := pendingMoveTestHash("a1")
	source := createPendingMoveTestMessage(t, ctx, db, owner, ownerAccount, ownerSource, 1, hash)
	markerID, err := db.CreatePendingMoveNotification(ctx, owner.ID, source.ID, ownerInbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePendingMoveNotification(ctx, other.ID, markerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant marker delete error = %v, want not found", err)
	}
	if _, err := db.CreatePendingMoveNotification(ctx, owner.ID, source.ID, ownerOtherInbox.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account destination error = %v, want not found", err)
	}
	if _, err := db.CreatePendingMoveNotification(ctx, owner.ID, source.ID, otherInbox.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant destination error = %v, want not found", err)
	}

	otherCandidate := createPendingMoveTestMessage(t, ctx, db, other, otherAccount, otherInbox, 11, hash)
	assertPendingMoveEventCreated(t, ctx, db, other.ID, otherCandidate)
	otherAccountCandidate := createPendingMoveTestMessage(t, ctx, db, owner, ownerOtherAccount, ownerOtherInbox, 12, hash)
	assertPendingMoveEventCreated(t, ctx, db, owner.ID, otherAccountCandidate)
	otherMailboxCandidate := createPendingMoveTestMessage(t, ctx, db, owner, ownerAccount, ownerArchive, 13, hash)
	assertPendingMoveEventCreated(t, ctx, db, owner.ID, otherMailboxCandidate)
	otherHashCandidate := createPendingMoveTestMessage(t, ctx, db, owner, ownerAccount, ownerInbox, 14, pendingMoveTestHash("b2"))
	assertPendingMoveEventCreated(t, ctx, db, owner.ID, otherHashCandidate)

	matching := createPendingMoveTestMessage(t, ctx, db, owner, ownerAccount, ownerInbox, 15, hash)
	if event, created, err := db.RecordNewMailEvent(ctx, owner.ID, matching); err != nil || created || event.ID != 0 {
		t.Fatalf("matching move event = %+v created=%t err=%v, want suppressed", event, created, err)
	}
	var consumedMessageID int64
	if err := db.DB().QueryRowContext(ctx, `SELECT consumed_message_id
		FROM pending_move_notifications WHERE user_id = ? AND id = ?`, owner.ID, markerID).Scan(&consumedMessageID); err != nil {
		t.Fatal(err)
	}
	if consumedMessageID != matching.ID {
		t.Fatalf("consumed message id = %d, want %d", consumedMessageID, matching.ID)
	}
}

func TestPendingMoveNotificationCardinalityAndRetryIdempotency(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user := createPendingMoveTestUser(t, ctx, db, "move-count@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	sourceMailbox := createPendingMoveTestMailbox(t, ctx, db, user, account, "Spam")
	destination := createPendingMoveTestMailbox(t, ctx, db, user, account, "INBOX")
	hash := pendingMoveTestHash("c3")
	for uid := uint32(1); uid <= 2; uid++ {
		source := createPendingMoveTestMessage(t, ctx, db, user, account, sourceMailbox, uid, hash)
		if _, err := db.CreatePendingMoveNotification(ctx, user.ID, source.ID, destination.ID); err != nil {
			t.Fatal(err)
		}
	}

	first := createPendingMoveTestMessage(t, ctx, db, user, account, destination, 101, hash)
	second := createPendingMoveTestMessage(t, ctx, db, user, account, destination, 102, hash)
	third := createPendingMoveTestMessage(t, ctx, db, user, account, destination, 103, hash)
	assertPendingMoveEventSuppressed(t, ctx, db, user.ID, first)
	assertPendingMoveEventSuppressed(t, ctx, db, user.ID, first)
	assertPendingMoveEventSuppressed(t, ctx, db, user.ID, second)
	assertPendingMoveEventCreated(t, ctx, db, user.ID, third)

	var pending, consumed int
	if err := db.DB().QueryRowContext(ctx, `SELECT
		COUNT(*) FILTER (WHERE consumed_message_id IS NULL),
		COUNT(*) FILTER (WHERE consumed_message_id IS NOT NULL)
		FROM pending_move_notifications WHERE user_id = ?`, user.ID).Scan(&pending, &consumed); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || consumed != 2 {
		t.Fatalf("marker cardinality pending=%d consumed=%d, want 0/2", pending, consumed)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(events) != 1 || events[0].MessageID != third.ID {
		t.Fatalf("new mail events = %+v count=%d, want only third message", events, count)
	}
}

func TestPendingMoveNotificationCanBeRemovedAndExpires(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user := createPendingMoveTestUser(t, ctx, db, "move-expiry@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	sourceMailbox := createPendingMoveTestMailbox(t, ctx, db, user, account, "Spam")
	destination := createPendingMoveTestMailbox(t, ctx, db, user, account, "INBOX")

	removedHash := pendingMoveTestHash("d4")
	removedSource := createPendingMoveTestMessage(t, ctx, db, user, account, sourceMailbox, 1, removedHash)
	removedID, err := db.CreatePendingMoveNotification(ctx, user.ID, removedSource.ID, destination.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePendingMoveNotification(ctx, user.ID, removedID); err != nil {
		t.Fatal(err)
	}
	removedCandidate := createPendingMoveTestMessage(t, ctx, db, user, account, destination, 101, removedHash)
	assertPendingMoveEventCreated(t, ctx, db, user.ID, removedCandidate)

	expiredHash := pendingMoveTestHash("e5")
	expiredSource := createPendingMoveTestMessage(t, ctx, db, user, account, sourceMailbox, 2, expiredHash)
	expiredID, err := db.CreatePendingMoveNotification(ctx, user.ID, expiredSource.ID, destination.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE pending_move_notifications
		SET expires_at = ? WHERE user_id = ? AND id = ?`, nowUnix()-1, user.ID, expiredID); err != nil {
		t.Fatal(err)
	}
	expiredCandidate := createPendingMoveTestMessage(t, ctx, db, user, account, destination, 102, expiredHash)
	assertPendingMoveEventCreated(t, ctx, db, user.ID, expiredCandidate)
	var expiredCount int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_move_notifications
		WHERE user_id = ? AND id = ?`, user.ID, expiredID).Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 0 {
		t.Fatalf("expired marker count = %d, want cleanup", expiredCount)
	}
}

func TestPendingMoveNotificationConsumptionAndEventDecisionAreAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user := createPendingMoveTestUser(t, ctx, db, "move-atomic@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	sourceMailbox := createPendingMoveTestMailbox(t, ctx, db, user, account, "Spam")
	destination := createPendingMoveTestMailbox(t, ctx, db, user, account, "INBOX")
	hash := pendingMoveTestHash("f6")
	source := createPendingMoveTestMessage(t, ctx, db, user, account, sourceMailbox, 1, hash)
	if _, err := db.CreatePendingMoveNotification(ctx, user.ID, source.ID, destination.ID); err != nil {
		t.Fatal(err)
	}
	candidates := []MessageRecord{
		createPendingMoveTestMessage(t, ctx, db, user, account, destination, 101, hash),
		createPendingMoveTestMessage(t, ctx, db, user, account, destination, 102, hash),
	}

	type result struct {
		messageID int64
		created   bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, created, err := db.RecordNewMailEvent(ctx, user.ID, candidate)
			results <- result{messageID: candidate.ID, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	createdCount := 0
	suppressedID := int64(0)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		} else {
			suppressedID = result.messageID
		}
	}
	if createdCount != 1 || suppressedID == 0 {
		t.Fatalf("concurrent decisions created=%d suppressed_id=%d, want one of each", createdCount, suppressedID)
	}
	var consumedMessageID int64
	if err := db.DB().QueryRowContext(ctx, `SELECT consumed_message_id
		FROM pending_move_notifications WHERE user_id = ?`, user.ID).Scan(&consumedMessageID); err != nil {
		t.Fatal(err)
	}
	if consumedMessageID != suppressedID {
		t.Fatalf("marker consumed by %d, suppressed result was %d", consumedMessageID, suppressedID)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(events) != 1 || events[0].MessageID == suppressedID {
		t.Fatalf("atomic events = %+v count=%d suppressed_id=%d", events, count, suppressedID)
	}
}

func createPendingMoveTestUser(t *testing.T, ctx context.Context, db *Store, email string) User {
	t.Helper()
	user, err := db.CreateUser(ctx, email, email, "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func createPendingMoveTestAccount(t *testing.T, ctx context.Context, db *Store, user User, suffix string) MailAccount {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: suffix + "-" + user.Email, Label: suffix,
		Host: suffix + ".imap.example.test", Port: 993, Username: suffix + "-" + user.Email,
		EncryptedPassword: "encrypted", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func createPendingMoveTestMailbox(t *testing.T, ctx context.Context, db *Store, user User, account MailAccount, name string) Mailbox {
	t.Helper()
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	return mailbox
}

func createPendingMoveTestMessage(t *testing.T, ctx context.Context, db *Store, user User, account MailAccount, mailbox Mailbox, uid uint32, hash string) MessageRecord {
	t.Helper()
	path := fmt.Sprintf("users/%d/pending-moves/accounts/%d/mailboxes/%d/uid-%d.eml", user.ID, account.ID, mailbox.ID, uid)
	blob, err := db.CreateBlob(ctx, BlobRecord{UserID: user.ID, Kind: "message", Path: path, SHA256: hash, Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		Subject: fmt.Sprintf("Message %d", uid), FromAddr: "sender@example.test",
		Date: time.Unix(int64(uid), 0).UTC(), InternalDate: time.Unix(int64(uid), 0).UTC(),
		UID: uid, Size: 1, BlobPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func pendingMoveTestHash(pair string) string {
	return strings.Repeat(pair, 32)
}

func assertPendingMoveEventCreated(t *testing.T, ctx context.Context, db *Store, userID int64, message MessageRecord) {
	t.Helper()
	event, created, err := db.RecordNewMailEvent(ctx, userID, message)
	if err != nil || !created || event.MessageID != message.ID {
		t.Fatalf("new mail event = %+v created=%t err=%v, want message %d", event, created, err, message.ID)
	}
}

func assertPendingMoveEventSuppressed(t *testing.T, ctx context.Context, db *Store, userID int64, message MessageRecord) {
	t.Helper()
	event, created, err := db.RecordNewMailEvent(ctx, userID, message)
	if err != nil || created || event.ID != 0 {
		t.Fatalf("new mail event = %+v created=%t err=%v, want suppressed", event, created, err)
	}
}
