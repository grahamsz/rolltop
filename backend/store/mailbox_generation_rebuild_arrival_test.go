package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestPendingInboxArrivalSurvivesGenerationResetAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, "arrival-rebuild@example.test", "Arrival Rebuild", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-rebuild", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailboxWithRole(ctx, user.ID, account.ID, "INBOX", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 8, 7001); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <arrival-rebuild@example.test>\r\nFrom: Sender <sender@example.test>\r\nSubject: Held arrival\r\n\r\nbody\r\n")
	original, fingerprint := createGenerationArrivalMessage(t, ctx, db, user.ID, account.ID,
		mailbox, 7, 7001, "<arrival-rebuild@example.test>", raw, base, "original")
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, run.ID, original, fingerprint, base)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("initial classification=%q, want pending", decision.Arrival.Classification)
	}

	stale, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 7002)
	if err != nil || !reset || len(stale) != 1 {
		t.Fatalf("generation reset stale=%d reset=%v err=%v", len(stale), reset, err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var journalRows, liveArrivals int
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_inbox_arrivals
		WHERE user_id = ?`, user.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_inbox_arrivals
		WHERE user_id = ?`, user.ID).Scan(&liveArrivals); err != nil {
		t.Fatal(err)
	}
	if journalRows != 1 || liveArrivals != 0 {
		t.Fatalf("after reset journal=%d live_arrivals=%d, want 1/0", journalRows, liveArrivals)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, account.ID, mailbox.ID, 7002)
	if err != nil || !pending {
		t.Fatalf("rebuild pending=%v err=%v after restart", pending, err)
	}

	// Identical content in another tenant must not consume or inherit this journal.
	otherUser, err := db.CreateUser(ctx, "arrival-rebuild-other@example.test", "Other Arrival", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: otherUser.ID, Email: otherUser.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-rebuild-other", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherMailbox, err := db.GetOrCreateMailboxWithRole(ctx, otherUser.ID, otherAccount.ID, "INBOX", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, otherUser.ID, otherMailbox.ID, 1, 0, 10, 7002); err != nil {
		t.Fatal(err)
	}
	otherMailbox, err = db.GetMailboxForUser(ctx, otherUser.ID, otherMailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	createGenerationArrivalMessage(t, ctx, db, otherUser.ID, otherAccount.ID,
		otherMailbox, 9, 7002, "<arrival-rebuild@example.test>", raw, base, "other-tenant")
	if _, err := db.NextPendingInboxArrivalDue(ctx, otherUser.ID, otherAccount.ID); !IsNotFound(err) {
		t.Fatalf("other tenant pending-arrival lookup error=%v, want not found", err)
	}

	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _ := createGenerationArrivalMessage(t, ctx, db, user.ID, account.ID,
		mailbox, 70, 7002, "<arrival-rebuild@example.test>", raw, base, "replacement")
	created, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("mid-rebuild timer created %d delivery events, want 0", created)
	}
	if _, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID); !IsNotFound(err) {
		t.Fatalf("mid-rebuild next due error=%v, want not found", err)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(events) != 0 {
		t.Fatalf("mid-rebuild timer emitted events=%+v count=%d", events, count)
	}
	schedules, err := db.ListPendingInboxArrivalSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, schedule := range schedules {
		if schedule.UserID == user.ID && schedule.AccountID == account.ID {
			t.Fatalf("mid-rebuild recovery exposed schedule=%+v", schedule)
		}
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, account.ID, mailbox.ID, 7002); err != nil {
		t.Fatal(err)
	}
	nextDue, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !nextDue.Equal(decision.Arrival.AvailableAt) {
		t.Fatalf("restored due=%v, want %v", nextDue, decision.Arrival.AvailableAt)
	}
	due, err := db.ListDueInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("restored due arrivals=%+v, want one", due)
	}
	restored := due[0]
	if restored.ID != decision.Arrival.ID || restored.MessageID != replacement.ID ||
		restored.SyncRunID != run.ID || restored.Classification != ArrivalPending ||
		restored.Fingerprint.RawSHA256 != fingerprint.RawSHA256 ||
		restored.Fingerprint.CanonicalSHA256 != fingerprint.CanonicalSHA256 ||
		restored.Fingerprint.MessageIDHash != fingerprint.MessageIDHash ||
		!restored.Fingerprint.InternalDate.Equal(fingerprint.InternalDate) ||
		restored.Fingerprint.Size != fingerprint.Size ||
		!restored.AvailableAt.Equal(decision.Arrival.AvailableAt) || !restored.FinalizedAt.IsZero() {
		t.Fatalf("restored arrival=%+v, want original state on message %d", restored, replacement.ID)
	}
	userDB, err = db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_inbox_arrivals
		WHERE user_id = ?`, user.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if journalRows != 0 {
		t.Fatalf("completed rebuild retained %d arrival journal rows", journalRows)
	}
	if _, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID); err != nil {
		t.Fatalf("completed rebuild removed restored live arrival: %v", err)
	}
}

func TestPendingInboxArrivalRebuildFailsClosedForAmbiguousDuplicate(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-rebuild-duplicate@example.test", "Arrival Duplicate", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-rebuild-duplicate", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailboxWithRole(ctx, user.ID, account.ID, "INBOX", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 2, 0, 3, 8001); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <duplicate@example.test>\r\nSubject: Identical duplicate\r\n\r\nbody\r\n")
	for i, uid := range []uint32{1, 2} {
		message, fingerprint := createGenerationArrivalMessage(t, ctx, db, user.ID, account.ID,
			mailbox, uid, 8001, "<duplicate@example.test>", raw, base, fmt.Sprintf("duplicate-%d", i))
		if i == 0 {
			if _, err := db.DB().ExecContext(ctx, `UPDATE messages
				SET is_read = 1, read_sync_pending = 1 WHERE user_id = ? AND id = ?`, user.ID, message.ID); err != nil {
				t.Fatal(err)
			}
		}
		decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, message, fingerprint, base)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Arrival.Classification != ArrivalPending {
			t.Fatalf("duplicate %d classification=%q, want pending", i, decision.Arrival.Classification)
		}
	}
	if _, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 8002); err != nil || !reset {
		t.Fatalf("generation reset reset=%v err=%v", reset, err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, _ := createGenerationArrivalMessage(t, ctx, db, user.ID, account.ID,
		mailbox, 1, 8002, "<duplicate@example.test>", raw, base, "ambiguous-refetch")
	if replacement.IsRead || replacement.ReadSyncPending {
		t.Fatalf("reused UID attached ambiguous read state: %+v", replacement)
	}
	if _, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID); !IsNotFound(err) {
		t.Fatalf("ambiguous refetch pending-arrival lookup error=%v, want not found", err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var journalRows int
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_inbox_arrivals
		WHERE user_id = ?`, user.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if journalRows != 2 {
		t.Fatalf("ambiguous refetch retained %d journal rows, want both candidates", journalRows)
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, account.ID, mailbox.ID, 8002); err != nil {
		t.Fatal(err)
	}
	if err := userDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailbox_generation_rebuild_inbox_arrivals
		WHERE user_id = ?`, user.ID).Scan(&journalRows); err != nil {
		t.Fatal(err)
	}
	if journalRows != 0 {
		t.Fatalf("completed rebuild retained %d ambiguous journal rows", journalRows)
	}
	if _, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID); !IsNotFound(err) {
		t.Fatalf("completed ambiguous rebuild attached an arrival: %v", err)
	}
}

func TestPendingInboxArrivalCanonicalRebuildUsesRefetchedFingerprint(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-rebuild-canonical@example.test", "Arrival Canonical", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-rebuild-canonical", EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX",
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailboxWithRole(ctx, user.ID, account.ID, "INBOX", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 0, 2, 8101); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	oldRaw := []byte("Message-ID: <canonical@example.test>\r\nSubject: Canonical\r\n\r\nbody\r\n")
	newRaw := []byte("Message-ID: <canonical@example.test>\nSubject: Canonical\n\nbody\n")
	original, oldFingerprint := createGenerationArrivalMessage(t, ctx, db, user.ID, account.ID,
		mailbox, 1, 8101, "<canonical@example.test>", oldRaw, base, "canonical-original")
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, original, oldFingerprint, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, reset, err := db.ResetMailboxForRemoteUIDValidity(ctx, user.ID, account.ID, mailbox.ID, 8102); err != nil || !reset {
		t.Fatalf("generation reset reset=%v err=%v", reset, err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, newFingerprint := createGenerationArrivalMessage(t, ctx, db, user.ID, account.ID,
		mailbox, 7, 8102, "<canonical@example.test>", newRaw, base, "canonical-refetch")
	if oldFingerprint.RawSHA256 == newFingerprint.RawSHA256 ||
		oldFingerprint.CanonicalSHA256 != newFingerprint.CanonicalSHA256 {
		t.Fatalf("test fingerprints old=%+v new=%+v, want raw difference and canonical equality", oldFingerprint, newFingerprint)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var restoredRaw, restoredCanonical string
	var restoredSize int64
	if err := userDB.QueryRowContext(ctx, `SELECT raw_sha256, canonical_sha256, message_size
		FROM pending_inbox_arrivals WHERE user_id = ? AND message_id = ?`, user.ID, replacement.ID).
		Scan(&restoredRaw, &restoredCanonical, &restoredSize); err != nil {
		t.Fatal(err)
	}
	if restoredRaw != newFingerprint.RawSHA256 || restoredCanonical != newFingerprint.CanonicalSHA256 || restoredSize != newFingerprint.Size {
		t.Fatalf("restored fingerprint raw=%q canonical=%q size=%d, want refetched %+v",
			restoredRaw, restoredCanonical, restoredSize, newFingerprint)
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, account.ID, mailbox.ID, 8102); err != nil {
		t.Fatal(err)
	}
	created, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt)
	if err != nil || created != 1 {
		t.Fatalf("canonical restored arrival finalization created=%d err=%v, want one", created, err)
	}
}

func createGenerationArrivalMessage(t *testing.T, ctx context.Context, db *Store,
	userID, accountID int64, mailbox Mailbox, uid uint32, uidValidity int64,
	messageID string, raw []byte, internalDate time.Time, suffix string,
) (MessageRecord, ArrivalFingerprint) {
	t.Helper()
	fingerprint := MessageArrivalFingerprint(raw, messageID, internalDate, int64(len(raw)))
	blob, err := db.CreateBlob(ctx, BlobRecord{
		UserID: userID, Kind: "message-remote",
		Path:   fmt.Sprintf("users/%d/generation-arrival-%s.eml", userID, suffix),
		SHA256: fingerprint.RawSHA256, Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: userID, AccountID: accountID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: messageID, MessageIDHash: fingerprint.MessageIDHash,
		CanonicalSHA256: fingerprint.CanonicalSHA256, Subject: "Held arrival",
		FromAddr: "Sender <sender@example.test>", Date: internalDate, InternalDate: internalDate,
		UID: uid, UIDValidity: uidValidity, Size: int64(len(raw)), BlobPath: blob.Path, BodyText: "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	return message, fingerprint
}
