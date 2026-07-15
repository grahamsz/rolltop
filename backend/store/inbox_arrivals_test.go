package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMessageArrivalFingerprintNormalizesOnlyLineEndings(t *testing.T) {
	internalDate := time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)
	crlf := []byte("Message-ID: <Example@EXAMPLE.test>\r\nSubject: Test\r\n\r\nBody\r\n")
	lf := []byte("Message-ID: <Example@EXAMPLE.test>\nSubject: Test\n\nBody\n")
	first := MessageArrivalFingerprint(crlf, " <Example@EXAMPLE.test> ", internalDate, int64(len(crlf)))
	second := MessageArrivalFingerprint(lf, "<example@example.test>", internalDate, int64(len(lf)))
	if first.RawSHA256 == second.RawSHA256 {
		t.Fatal("exact digests unexpectedly match across line-ending changes")
	}
	if first.CanonicalSHA256 != second.CanonicalSHA256 {
		t.Fatalf("canonical digests differ: %q != %q", first.CanonicalSHA256, second.CanonicalSHA256)
	}
	if first.MessageIDHash != second.MessageIDHash || first.MessageIDHash == "" {
		t.Fatalf("normalized Message-ID digests differ: %q != %q", first.MessageIDHash, second.MessageIDHash)
	}
	changed := MessageArrivalFingerprint([]byte(strings.ReplaceAll(string(lf), "Body", "body")),
		"<example@example.test>", internalDate, int64(len(lf)))
	if changed.CanonicalSHA256 == second.CanonicalSHA256 {
		t.Fatal("canonical digest ignored a non-line-ending content change")
	}
}

func TestInboxArrivalLocalTransferUsesReceiptAndFailsOpenOnMismatch(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-local@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 91)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 92)
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	raw := []byte("Message-ID: <receipt@example.test>\r\nSubject: Receipt\r\n\r\nSame body\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 7, raw,
		"<receipt@example.test>", "thread:receipt", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 200, inbox.UIDValidity); err != nil {
		t.Fatal(err)
	}

	wrongUID, wrongFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 201,
		raw, "<receipt@example.test>", "thread:receipt", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, wrongUID, wrongFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("receipt mismatch classified %q, want pending", decision.Arrival.Classification)
	}
	actual, actualFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 200,
		raw, "<receipt@example.test>", "thread:receipt", now)
	if _, err := db.SnoozeMessage(ctx, user.ID, actual.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	decision, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, actual, actualFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalLocalMove || decision.EventCreated {
		t.Fatalf("actual receipt decision = %+v", decision)
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, actual.ID); err != nil {
		t.Fatalf("local move cancelled snooze: %v", err)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil || count != 0 || len(events) != 0 {
		t.Fatalf("local move events=%+v count=%d err=%v", events, count, err)
	}
}

func TestProvenPendingMoveSurvivesGenericExpungeWindow(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-delayed-pending-move@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 93)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 94)
	now := time.Now().UTC()
	raw := []byte("Message-ID: <delayed-pending@example.test>\r\nSubject: Delayed pending move\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 8, raw,
		"<delayed-pending@example.test>", "thread:delayed-pending", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimMessageTransferDispatch(ctx, user.ID, transfer.ID); err != nil || !claimed {
		t.Fatalf("claim dispatched move=%v err=%v", claimed, err)
	}

	deleted, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), source.UID+1, nil)
	if err != nil || len(deleted) != 1 || deleted[0].ID != source.ID {
		t.Fatalf("proven source deletion=%+v err=%v", deleted, err)
	}
	var state string
	if err := db.DB().QueryRow(`SELECT state FROM message_transfers WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" {
		t.Fatalf("proven pending move state=%q, want succeeded", state)
	}
	var tombstones int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ?`, user.ID, spam.ID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("proven pending move created %d short-lived tombstones", tombstones)
	}

	arrivalTime := now.Add(expungedFingerprintTTL + time.Second)
	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 9, raw,
		"<delayed-pending@example.test>", "thread:delayed-pending", arrivalTime)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, arrivalTime)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalLocalMove || decision.EventCreated {
		t.Fatalf("delayed destination decision=%+v, want durable local move", decision)
	}
}

func TestUndispatchedPendingMoveUsesGenericExternalMoveEvidence(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-undispatched-external@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 941)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 942)
	now := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <undispatched-external@example.test>\r\nSubject: External move\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 41, raw,
		"<undispatched-external@example.test>", "thread:undispatched-external", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), source.UID+1, nil)
	if err != nil || len(deleted) != 1 || deleted[0].ID != source.ID {
		t.Fatalf("external source deletion=%+v err=%v", deleted, err)
	}
	var state string
	var dispatchedAt int64
	if err := db.DB().QueryRowContext(ctx, `SELECT state, dispatched_at FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state, &dispatchedAt); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || dispatchedAt != 0 {
		t.Fatalf("undispatched transfer state=%q dispatched_at=%d, want pending/0", state, dispatchedAt)
	}
	var tombstones int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID, spam.ID,
		source.UID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("generic expunge evidence=%d, want 1", tombstones)
	}

	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 42, raw,
		"<undispatched-external@example.test>", "thread:undispatched-external", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalExternalMove || decision.EventCreated {
		t.Fatalf("undispatched source disappearance decision=%+v, want generic external move", decision)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT state FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("generic external move consumed transfer state=%q, want pending", state)
	}
}

func TestUndispatchedPendingMoveDoesNotSuppressLateIdenticalInboxDelivery(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-undispatched-late@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 943)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 944)
	now := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <undispatched-late@example.test>\r\nSubject: Late delivery\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 43, raw,
		"<undispatched-late@example.test>", "thread:undispatched-late", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), source.UID+1, nil)
	if err != nil || len(deleted) != 1 || deleted[0].ID != source.ID {
		t.Fatalf("external source deletion=%+v err=%v", deleted, err)
	}
	lateAt := now.Add(messageTransferTTL + time.Hour)
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_transfers SET expires_at = ?
		WHERE user_id = ? AND id = ?`, lateAt.Add(-time.Second).Unix(), user.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE expunged_message_fingerprints SET expires_at = ?
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, lateAt.Add(-time.Second).Unix(),
		user.ID, spam.ID, source.UID); err != nil {
		t.Fatal(err)
	}

	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 44, raw,
		"<undispatched-late@example.test>", "thread:undispatched-late", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, lateAt)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalPending || decision.EventCreated {
		t.Fatalf("late identical arrival decision=%+v, want pending delivery", decision)
	}
	created, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt)
	if err != nil || created != 1 {
		t.Fatalf("late identical arrival finalization created=%d err=%v, want 1", created, err)
	}
	_, eventCount, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil || eventCount != 1 {
		t.Fatalf("late identical delivery events=%d err=%v, want 1", eventCount, err)
	}
	var state string
	var dispatchedAt int64
	if err := db.DB().QueryRowContext(ctx, `SELECT state, dispatched_at FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state, &dispatchedAt); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || dispatchedAt != 0 {
		t.Fatalf("late delivery changed undispatched transfer state=%q dispatched_at=%d", state, dispatchedAt)
	}
	var tombstones int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID, spam.ID,
		source.UID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("expired generic expunge evidence retained=%d, want 0", tombstones)
	}
}

func TestPendingArrivalRetainsRacingExpungeEvidenceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := createPendingMoveTestUser(t, ctx, db, "arrival-racing-expunge@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 945)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 946)
	now := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <racing-expunge@example.test>\r\nSubject: Racing expunge\r\n\r\nBody\r\n")
	source, sourceFingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 45, raw,
		"<racing-expunge@example.test>", "thread:racing-expunge", now)
	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 46, raw,
		"<racing-expunge@example.test>", "thread:racing-expunge", now)
	held, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if held.Arrival.Classification != ArrivalPending {
		t.Fatalf("initial arrival classification=%q, want pending", held.Arrival.Classification)
	}
	candidates, err := db.ListPotentialMoveSources(ctx, user.ID, arrival.ID, 20)
	if err != nil || len(candidates) != 1 || candidates[0].Message.ID != source.ID {
		t.Fatalf("pre-reconcile source probe=%+v err=%v", candidates, err)
	}
	deleted, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), source.UID+1,
		map[int64]string{source.ID: sourceFingerprint.CanonicalSHA256})
	if err != nil || len(deleted) != 1 || deleted[0].ID != source.ID {
		t.Fatalf("atomic source reconcile=%+v err=%v", deleted, err)
	}
	var createdDelta int64
	if err := db.DB().QueryRowContext(ctx, `SELECT ABS(arrival.created_at - fingerprint.created_at)
		FROM pending_inbox_arrivals arrival, expunged_message_fingerprints fingerprint
		WHERE arrival.user_id = ? AND arrival.message_id = ? AND fingerprint.user_id = ?
			AND fingerprint.source_mailbox_id = ? AND fingerprint.source_uid = ?`, user.ID,
		arrival.ID, user.ID, spam.ID, source.UID).Scan(&createdDelta); err != nil {
		t.Fatal(err)
	}
	if createdDelta > int64(expungedFingerprintTTL/time.Second) {
		t.Fatalf("race evidence created delta=%ds exceeds correlation window", createdDelta)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	finalizedAt := now.Add(2 * expungedFingerprintTTL)
	created, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, finalizedAt)
	if err != nil || created != 0 {
		t.Fatalf("post-restart finalization created=%d err=%v, want no delivery", created, err)
	}
	var classification string
	var matchedExpungedID int64
	if err := db.DB().QueryRowContext(ctx, `SELECT classification, matched_expunged_id
		FROM pending_inbox_arrivals WHERE user_id = ? AND message_id = ?`, user.ID,
		arrival.ID).Scan(&classification, &matchedExpungedID); err != nil {
		t.Fatal(err)
	}
	if classification != string(ArrivalExternalMove) || matchedExpungedID == 0 {
		t.Fatalf("post-restart classification=%q matched_expunged_id=%d, want external move", classification, matchedExpungedID)
	}
	_, eventCount, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 5)
	if err != nil || eventCount != 0 {
		t.Fatalf("post-restart delivery events=%d err=%v, want 0", eventCount, err)
	}
}

func TestUnconsumedTransferEvidenceSurvivesOfflineExpiryAndIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	owner := createPendingMoveTestUser(t, ctx, db, "arrival-offline-owner@example.test")
	other := createPendingMoveTestUser(t, ctx, db, "arrival-offline-other@example.test")
	ownerAccount := createPendingMoveTestAccount(t, ctx, db, owner, "primary")
	otherAccount := createPendingMoveTestAccount(t, ctx, db, other, "primary")
	ownerSource := arrivalTestMailbox(t, ctx, db, owner, ownerAccount, "Spam", 951)
	ownerInbox := arrivalTestMailbox(t, ctx, db, owner, ownerAccount, "INBOX", 952)
	otherInbox := arrivalTestMailbox(t, ctx, db, other, otherAccount, "INBOX", 952)
	started := time.Now().UTC().Truncate(time.Second)
	arrivalTime := started.Add(messageTransferTTL + time.Hour)
	raw := []byte("Message-ID: <offline-transfer@example.test>\r\nSubject: Offline transfer\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, owner, ownerAccount, ownerSource, 51, raw,
		"<offline-transfer@example.test>", "thread:offline-transfer", started)
	transfer, err := db.StageMessageTransfer(ctx, owner.ID, source.ID, ownerInbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, owner.ID, transfer.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_transfers SET expires_at = ?
		WHERE user_id = ? AND id = ?`, arrivalTime.Add(-time.Second).Unix(), owner.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}

	ownerArrival, ownerFingerprint := arrivalTestMessage(t, ctx, db, owner, ownerAccount, ownerInbox, 52, raw,
		"<offline-transfer@example.test>", "thread:offline-transfer", started)
	ownerDecision, err := db.HoldOrClassifyInboxArrival(ctx, owner.ID, 0, ownerArrival, ownerFingerprint, arrivalTime)
	if err != nil {
		t.Fatal(err)
	}
	if ownerDecision.Arrival.Classification != ArrivalLocalMove || ownerDecision.EventCreated {
		t.Fatalf("offline owner decision=%+v, want local move without notification", ownerDecision)
	}

	otherArrival, otherFingerprint := arrivalTestMessage(t, ctx, db, other, otherAccount, otherInbox, 52, raw,
		"<offline-transfer@example.test>", "thread:offline-transfer", started)
	otherDecision, err := db.HoldOrClassifyInboxArrival(ctx, other.ID, 0, otherArrival, otherFingerprint, arrivalTime)
	if err != nil {
		t.Fatal(err)
	}
	if otherDecision.Arrival.Classification != ArrivalPending {
		t.Fatalf("other tenant classification=%q, want pending delivery", otherDecision.Arrival.Classification)
	}
	created, err := db.FinalizeDueInboxArrivals(ctx, other.ID, otherAccount.ID, otherDecision.Arrival.AvailableAt)
	if err != nil || created != 1 {
		t.Fatalf("other tenant finalization created=%d err=%v", created, err)
	}
	_, ownerEventCount, _, err := db.NewMailEventsAfter(ctx, owner.ID, 0, 5)
	if err != nil || ownerEventCount != 0 {
		t.Fatalf("owner events=%d err=%v, want 0", ownerEventCount, err)
	}
	_, otherEventCount, _, err := db.NewMailEventsAfter(ctx, other.ID, 0, 5)
	if err != nil || otherEventCount != 1 {
		t.Fatalf("other events=%d err=%v, want 1", otherEventCount, err)
	}

	// Once consumed, the already-old transfer is terminal and the next owner
	// arrival may prune it without risking a false notification.
	cleanupRaw := []byte("Message-ID: <offline-cleanup@example.test>\r\n\r\ncleanup\r\n")
	cleanupMessage, cleanupFingerprint := arrivalTestMessage(t, ctx, db, owner, ownerAccount, ownerInbox, 53,
		cleanupRaw, "<offline-cleanup@example.test>", "thread:offline-cleanup", arrivalTime)
	if _, err := db.HoldOrClassifyInboxArrival(ctx, owner.ID, 0, cleanupMessage, cleanupFingerprint, arrivalTime); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_transfers
		WHERE user_id = ? AND id = ?`, owner.ID, transfer.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 0 {
		t.Fatalf("old consumed transfer retained=%d, want pruned", retained)
	}
}

func TestExpiredDispatchedPendingTransferCannotBeRestagedOrRedispatched(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-expired-pending@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	sourceMailbox := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 961)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 962)
	raw := []byte("Message-ID: <expired-pending@example.test>\r\n\r\nBody\r\n")
	message, fingerprint := arrivalTestMessage(t, ctx, db, user, account, sourceMailbox, 61, raw,
		"<expired-pending@example.test>", "thread:expired-pending", time.Now().UTC())
	transfer, err := db.StageMessageTransfer(ctx, user.ID, message.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := db.ClaimMessageTransferDispatchForOwner(ctx, user.ID, transfer.ID, "test-process")
	if err != nil || !claimed {
		t.Fatalf("initial dispatch claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if err := db.FinishMessageTransferDispatch(ctx, user.ID, transfer.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_transfers SET expires_at = ?
		WHERE user_id = ? AND id = ?`, nowUnix()-1, user.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := db.StageMessageTransfer(ctx, user.ID, message.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != transfer.ID || retry.State != "pending" || retry.WasCreated || retry.DispatchedAt.IsZero() {
		t.Fatalf("expired pending retry=%+v, want original pending transfer", retry)
	}
	if claimed, err := db.ClaimMessageTransferDispatch(ctx, user.ID, transfer.ID); err != nil || claimed {
		t.Fatalf("expired pending repeat claim=%v err=%v, want false/nil", claimed, err)
	}
}

func TestExpiredUnknownMoveRetainsExactSourceExpungeForLateInboxArrival(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-expired-unknown-move@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	sourceMailbox := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 963)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 964)
	started := time.Now().UTC().Truncate(time.Second)
	arrivalTime := started.Add(messageTransferTTL + time.Hour)
	raw := []byte("Message-ID: <expired-unknown-move@example.test>\r\nSubject: Unknown move\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, sourceMailbox, 63, raw,
		"<expired-unknown-move@example.test>", "thread:expired-unknown-move", started)
	// Model the narrow reconciliation race in which the exact source tombstone
	// commits just before the durable pending transfer becomes visible.
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, source.ID, fingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := db.ClaimMessageTransferDispatchForOwner(ctx, user.ID, transfer.ID, "crashed-process")
	if err != nil || !claimed {
		t.Fatalf("unknown move claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if err := db.FinishMessageTransferDispatch(ctx, user.ID, transfer.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM messages WHERE user_id = ? AND id = ?`, user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	old := arrivalTime.Add(-time.Second).Unix()
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_transfers SET expires_at = ?
		WHERE user_id = ? AND id = ?`, old, user.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE expunged_message_fingerprints SET expires_at = ?
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, old,
		user.ID, sourceMailbox.ID, source.UID); err != nil {
		t.Fatal(err)
	}

	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 64, raw,
		"<expired-unknown-move@example.test>", "thread:expired-unknown-move", started)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, arrivalTime)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalLocalMove || decision.EventCreated {
		t.Fatalf("late unknown move decision=%+v, want local move without notification", decision)
	}
	var transferState string
	var expungeConsumedAt int64
	if err := db.DB().QueryRowContext(ctx, `SELECT state FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&transferState); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT consumed_at FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID,
		sourceMailbox.ID, source.UID).Scan(&expungeConsumedAt); err != nil {
		t.Fatal(err)
	}
	if transferState != "consumed" || expungeConsumedAt == 0 {
		t.Fatalf("late unknown evidence transfer=%q expunge_consumed_at=%d", transferState, expungeConsumedAt)
	}
}

func TestUnconsumedTransferLimitIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	limited := createPendingMoveTestUser(t, ctx, db, "arrival-transfer-limit@example.test")
	other := createPendingMoveTestUser(t, ctx, db, "arrival-transfer-limit-other@example.test")
	limitedAccount := createPendingMoveTestAccount(t, ctx, db, limited, "primary")
	otherAccount := createPendingMoveTestAccount(t, ctx, db, other, "primary")
	limitedSource := arrivalTestMailbox(t, ctx, db, limited, limitedAccount, "Source", 971)
	limitedDestination := arrivalTestMailbox(t, ctx, db, limited, limitedAccount, "Destination", 972)
	otherSource := arrivalTestMailbox(t, ctx, db, other, otherAccount, "Source", 971)
	otherDestination := arrivalTestMailbox(t, ctx, db, other, otherAccount, "Destination", 972)

	tx, err := db.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO message_transfers
		(user_id, source_account_id, destination_account_id, source_mailbox_id, destination_mailbox_id,
		 source_uid, source_uid_validity, operation_kind, state, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'move', 'succeeded', ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	past := nowUnix() - 1
	for i := 0; i < maxUnconsumedMessageTransfersPerUser; i++ {
		if _, err := stmt.ExecContext(ctx, limited.ID, limitedAccount.ID, limitedAccount.ID,
			limitedSource.ID, limitedDestination.ID, i+1, limitedSource.UIDValidity, past, past, past); err != nil {
			t.Fatalf("seed active transfer %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	limitedRaw := []byte("Message-ID: <limited-transfer@example.test>\r\n\r\nBody\r\n")
	limitedMessage, limitedFingerprint := arrivalTestMessage(t, ctx, db, limited, limitedAccount,
		limitedSource, uint32(maxUnconsumedMessageTransfersPerUser+1), limitedRaw,
		"<limited-transfer@example.test>", "thread:limited-transfer", time.Now().UTC())
	if _, err := db.StageMessageTransfer(ctx, limited.ID, limitedMessage.ID, limitedDestination.ID,
		"move", limitedFingerprint.CanonicalSHA256); err == nil || !strings.Contains(err.Error(), "too many unresolved") {
		t.Fatalf("limited tenant stage error=%v", err)
	}

	otherRaw := []byte("Message-ID: <other-transfer@example.test>\r\n\r\nBody\r\n")
	otherMessage, otherFingerprint := arrivalTestMessage(t, ctx, db, other, otherAccount, otherSource, 1,
		otherRaw, "<other-transfer@example.test>", "thread:other-transfer", time.Now().UTC())
	if _, err := db.StageMessageTransfer(ctx, other.ID, otherMessage.ID, otherDestination.ID,
		"move", otherFingerprint.CanonicalSHA256); err != nil {
		t.Fatalf("other tenant was limited: %v", err)
	}
}

func TestDispatchedUnknownMoveToNonInboxTerminalizesWithSourceReconcile(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-noninbox-unknown-move@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 973)
	archive := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 974)
	now := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <noninbox-unknown-move@example.test>\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 73, raw,
		"<noninbox-unknown-move@example.test>", "thread:noninbox-unknown-move", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, archive.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := db.ClaimMessageTransferDispatchForOwner(ctx, user.ID, transfer.ID, "crashed-process")
	if err != nil || !claimed {
		t.Fatalf("unknown move claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if err := db.FinishMessageTransferDispatch(ctx, user.ID, transfer.ID, claim); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), source.UID+1, nil)
	if err != nil || len(deleted) != 1 || deleted[0].ID != source.ID {
		t.Fatalf("non-Inbox source reconcile=%+v err=%v", deleted, err)
	}
	var state string
	var consumedAt, expiresAt int64
	if err := db.DB().QueryRowContext(ctx, `SELECT state, consumed_at, expires_at FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state, &consumedAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if state != "consumed" || consumedAt == 0 || expiresAt-consumedAt != int64(messageTransferTTL/time.Second) {
		t.Fatalf("non-Inbox unknown move state=%q consumed_at=%d expires_at=%d", state, consumedAt, expiresAt)
	}
	var tombstones int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID, spam.ID,
		source.UID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("non-Inbox proven move created %d generic tombstones", tombstones)
	}
}

func TestNonInboxCopyTerminalizationIsReusableAndBounded(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-noninbox-copy-lifecycle@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	sourceMailbox := arrivalTestMailbox(t, ctx, db, user, account, "Source", 975)
	archive := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 976)
	now := time.Now().UTC().Truncate(time.Second)
	raw := []byte("Message-ID: <noninbox-copy-lifecycle@example.test>\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, sourceMailbox, 75, raw,
		"<noninbox-copy-lifecycle@example.test>", "thread:noninbox-copy-lifecycle", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, archive.ID, "copy", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 76, archive.UIDValidity); err != nil {
		t.Fatal(err)
	}
	if err := db.TerminalizeMessageTransferWithoutArrival(ctx, user.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	var consumedAt, expiresAt int64
	if err := db.DB().QueryRowContext(ctx, `SELECT state, consumed_at, expires_at FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state, &consumedAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if state != "consumed" || consumedAt == 0 || expiresAt-consumedAt != int64(messageTransferTTL/time.Second) {
		t.Fatalf("terminal copy state=%q consumed_at=%d expires_at=%d", state, consumedAt, expiresAt)
	}
	retry, err := db.StageMessageTransfer(ctx, user.ID, source.ID, archive.ID, "copy", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != transfer.ID || retry.State != "consumed" || retry.WasCreated {
		t.Fatalf("unexpired terminal copy retry=%+v, want existing consumed transfer", retry)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE message_transfers SET expires_at = ?
		WHERE user_id = ? AND id = ?`, nowUnix()-1, user.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}
	repeated, err := db.StageMessageTransfer(ctx, user.ID, source.ID, archive.ID, "copy", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID == transfer.ID || repeated.State != "pending" || !repeated.WasCreated {
		t.Fatalf("expired terminal copy repeat=%+v, want fresh pending transfer", repeated)
	}
	var count int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_transfers
		WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("bounded copy transfer rows=%d, want 1", count)
	}
}

func TestStageMessageTransferReusesActiveSourceOperation(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-idempotent-transfer@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 95)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 96)
	archive := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 97)
	now := time.Date(2026, 7, 14, 14, 30, 0, 0, time.UTC)
	raw := []byte("Message-ID: <idempotent@example.test>\r\nSubject: Retry\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 9, raw,
		"<idempotent@example.test>", "thread:idempotent", now)
	first, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !first.WasCreated || second.WasCreated || first.ID != second.ID {
		t.Fatalf("retry staged transfer %d, want existing %d", second.ID, first.ID)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, first.ID, 10, inbox.UIDValidity); err != nil {
		t.Fatal(err)
	}
	third, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if third.WasCreated || third.ID != first.ID {
		t.Fatalf("succeeded retry staged transfer %d, want existing %d", third.ID, first.ID)
	}
	differentDestination, err := db.StageMessageTransfer(ctx, user.ID, source.ID, archive.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if differentDestination.WasCreated || differentDestination.ID != first.ID || differentDestination.DestinationMailboxID != inbox.ID {
		t.Fatalf("second move target created/replaced source operation: %+v", differentDestination)
	}
	var count int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM message_transfers WHERE user_id = ?`, user.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("active transfer count=%d, want 1", count)
	}
}

func TestClaimMessageTransferDispatchIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-dispatch-claim@example.test")
	other := createPendingMoveTestUser(t, ctx, db, "arrival-dispatch-claim-other@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 98)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 99)
	raw := []byte("Message-ID: <dispatch-claim@example.test>\r\nSubject: Claim\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 10, raw,
		"<dispatch-claim@example.test>", "thread:dispatch-claim", time.Now().UTC())
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !transfer.DispatchedAt.IsZero() {
		t.Fatalf("new transfer dispatched_at=%v, want zero", transfer.DispatchedAt)
	}
	retry, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if retry.WasCreated || retry.ID != transfer.ID || !retry.DispatchedAt.IsZero() {
		t.Fatalf("unclaimed staging retry = %+v, want same unclaimed transfer", retry)
	}

	const callers = 12
	type claimResult struct {
		claimed bool
		err     error
	}
	results := make(chan claimResult, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			ready.Done()
			<-start
			claimed, claimErr := db.ClaimMessageTransferDispatch(ctx, user.ID, transfer.ID)
			results <- claimResult{claimed: claimed, err: claimErr}
		}()
	}
	ready.Wait()
	close(start)
	claimedCount := 0
	for range callers {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.claimed {
			claimedCount++
		}
	}
	if claimedCount != 1 {
		t.Fatalf("successful dispatch claims=%d, want 1", claimedCount)
	}
	claimedTransfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if claimedTransfer.DispatchedAt.IsZero() {
		t.Fatal("dispatch claim was not persisted")
	}
	if claimed, err := db.ClaimMessageTransferDispatch(ctx, user.ID, transfer.ID); err != nil || claimed {
		t.Fatalf("repeat claim = %t, %v; want false, nil", claimed, err)
	}
	if claimed, err := db.ClaimMessageTransferDispatch(ctx, other.ID, transfer.ID); err != nil || claimed {
		t.Fatalf("cross-tenant claim = %t, %v; want false, nil", claimed, err)
	}
}

func TestInboxArrivalCanonicalTransferAndCrossAccountCopy(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-copy@example.test")
	other := createPendingMoveTestUser(t, ctx, db, "arrival-copy-other@example.test")
	sourceAccount := createPendingMoveTestAccount(t, ctx, db, user, "source")
	destinationAccount := createPendingMoveTestAccount(t, ctx, db, user, "destination")
	otherAccount := createPendingMoveTestAccount(t, ctx, db, other, "other")
	sourceMailbox := arrivalTestMailbox(t, ctx, db, user, sourceAccount, "Archive", 100)
	destination := arrivalTestMailbox(t, ctx, db, user, destinationAccount, "INBOX", 200)
	otherInbox := arrivalTestMailbox(t, ctx, db, other, otherAccount, "INBOX", 300)
	now := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	crlf := []byte("Message-ID: <copy@example.test>\r\nSubject: Copy\r\n\r\nBody\r\n")
	lf := []byte("Message-ID: <copy@example.test>\nSubject: Copy\n\nBody\n")
	source, sourceFingerprint := arrivalTestMessage(t, ctx, db, user, sourceAccount,
		sourceMailbox, 10, crlf, "<copy@example.test>", "thread:copy", now)
	if _, err := db.StageMessageTransfer(ctx, user.ID, source.ID, destination.ID, "move", sourceFingerprint.CanonicalSHA256); err == nil {
		t.Fatal("cross-account move staged")
	}
	if _, err := db.StageMessageTransfer(ctx, other.ID, source.ID, otherInbox.ID, "copy", sourceFingerprint.CanonicalSHA256); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant transfer error = %v, want not found", err)
	}
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, destination.ID, "copy", sourceFingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	destinationMessage, destinationFingerprint := arrivalTestMessage(t, ctx, db, user,
		destinationAccount, destination, 20, lf, "<copy@example.test>", "thread:copy", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, destinationMessage, destinationFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalLocalCopy {
		t.Fatalf("canonical copy classified %q", decision.Arrival.Classification)
	}
}

func TestInboxArrivalLocalTransferConsumesConcurrentExpungeEvidence(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-transfer-expunge@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 401)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 402)
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, time.UTC)
	raw := []byte("Message-ID: <transfer-expunge@example.test>\r\nSubject: Transfer\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 40, raw,
		"<transfer-expunge@example.test>", "thread:transfer-expunge", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimMessageTransferDispatch(ctx, user.ID, transfer.ID); err != nil || !claimed {
		t.Fatalf("claim dispatched move=%v err=%v", claimed, err)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, source.ID, fingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	var provenState string
	if err := db.DB().QueryRow(`SELECT state FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&provenState); err != nil {
		t.Fatal(err)
	}
	if provenState != "succeeded" {
		t.Fatalf("source disappearance left transfer state=%q, want succeeded", provenState)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 41, inbox.UIDValidity); err != nil {
		t.Fatal(err)
	}
	destination, destinationFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 41, raw,
		"<transfer-expunge@example.test>", "thread:transfer-expunge", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, destination, destinationFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalLocalMove {
		t.Fatalf("classification = %q, want %q", decision.Arrival.Classification, ArrivalLocalMove)
	}
	var transferState string
	var transferConsumedMessageID int64
	if err := db.DB().QueryRow(`SELECT state, consumed_message_id FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&transferState, &transferConsumedMessageID); err != nil {
		t.Fatal(err)
	}
	if transferState != "consumed" || transferConsumedMessageID != destination.ID {
		t.Fatalf("transfer state=%q consumed_message_id=%d", transferState, transferConsumedMessageID)
	}
	var expungeCount int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID, spam.ID, source.UID).
		Scan(&expungeCount); err != nil {
		t.Fatal(err)
	}
	if expungeCount != 0 {
		t.Fatalf("durably linked transfer also created %d generic expunge rows", expungeCount)
	}
}

func TestInboxArrivalConsumesPendingMoveProvenByExpunge(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-pending-move@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 451)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 452)
	now := time.Date(2026, 7, 14, 15, 40, 0, 0, time.UTC)
	raw := []byte("Message-ID: <pending-move@example.test>\r\nSubject: Pending\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 45, raw,
		"<pending-move@example.test>", "thread:pending-move", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimMessageTransferDispatch(ctx, user.ID, transfer.ID); err != nil || !claimed {
		t.Fatalf("claim dispatched move=%v err=%v", claimed, err)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, source.ID, fingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	destination, destinationFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 46, raw,
		"<pending-move@example.test>", "thread:pending-move", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, destination, destinationFingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalLocalMove {
		t.Fatalf("pending move decision=%+v err=%v", decision, err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 46, inbox.UIDValidity); err != nil {
		t.Fatalf("late MOVE success was not idempotent: %v", err)
	}
	var state string
	var consumedMessageID int64
	if err := db.DB().QueryRow(`SELECT state, consumed_message_id FROM message_transfers
		WHERE user_id = ? AND id = ?`, user.ID, transfer.ID).Scan(&state, &consumedMessageID); err != nil {
		t.Fatal(err)
	}
	if state != "consumed" || consumedMessageID != destination.ID {
		t.Fatalf("late transfer state=%q consumed_message_id=%d", state, consumedMessageID)
	}
	duplicate, duplicateFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 47, raw,
		"<pending-move@example.test>", "thread:pending-move", now.Add(time.Second))
	decision, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, duplicate, duplicateFingerprint, now.Add(time.Second))
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("duplicate after late MOVE decision=%+v err=%v", decision, err)
	}
}

func TestInboxArrivalCopyDoesNotConsumeUnrelatedMoveEvidence(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-copy-expunge@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	copySource := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 501)
	movedSource := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 502)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 503)
	now := time.Date(2026, 7, 14, 15, 45, 0, 0, time.UTC)
	raw := []byte("Message-ID: <copy-expunge@example.test>\r\nSubject: Duplicate\r\n\r\nSame body\r\n")
	copyMessage, fingerprint := arrivalTestMessage(t, ctx, db, user, account, copySource, 50, raw,
		"<copy-expunge@example.test>", "thread:copy-expunge", now)
	movedMessage, _ := arrivalTestMessage(t, ctx, db, user, account, movedSource, 51, raw,
		"<copy-expunge@example.test>", "thread:copy-expunge", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, copyMessage.ID, inbox.ID, "copy", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 52, inbox.UIDValidity); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, movedMessage.ID, fingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	copyArrival, copyFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 52, raw,
		"<copy-expunge@example.test>", "thread:copy-expunge", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, copyArrival, copyFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalLocalCopy {
		t.Fatalf("copy classification = %q", decision.Arrival.Classification)
	}
	var consumed sql.NullInt64
	if err := db.DB().QueryRow(`SELECT consumed_message_id FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID, movedSource.ID, movedMessage.UID).
		Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed.Valid {
		t.Fatalf("copy consumed unrelated move evidence for message %d", consumed.Int64)
	}

	moveArrival, moveFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 53, raw,
		"<copy-expunge@example.test>", "thread:copy-expunge", now)
	decision, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, moveArrival, moveFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Arrival.Classification != ArrivalExternalMove {
		t.Fatalf("move classification = %q, want %q", decision.Arrival.Classification, ArrivalExternalMove)
	}
}

func TestConsumedLocalMoveDoesNotRecreateExpungeEvidence(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-consumed-move@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 601)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 602)
	now := time.Date(2026, 7, 14, 15, 50, 0, 0, time.UTC)
	raw := []byte("Message-ID: <consumed-move@example.test>\r\nSubject: Move\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 60, raw,
		"<consumed-move@example.test>", "thread:consumed-move", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, inbox.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 61, inbox.UIDValidity); err != nil {
		t.Fatal(err)
	}
	moved, movedFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 61, raw,
		"<consumed-move@example.test>", "thread:consumed-move", now)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, moved, movedFingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalLocalMove {
		t.Fatalf("local move decision=%+v err=%v", decision, err)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, source.ID, fingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	var tombstones int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`, user.ID, spam.ID, source.UID).
		Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("consumed move recreated %d expunge rows", tombstones)
	}
	duplicate, duplicateFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 62, raw,
		"<consumed-move@example.test>", "thread:consumed-move", now.Add(time.Second))
	decision, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, duplicate, duplicateFingerprint, now.Add(time.Second))
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("genuine duplicate decision=%+v err=%v", decision, err)
	}
}

func TestKnownMoveOutsideInboxDoesNotCreateGenericExpungeEvidence(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-noninbox-move@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 611)
	archive := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 612)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 613)
	now := time.Date(2026, 7, 14, 15, 55, 0, 0, time.UTC)
	raw := []byte("Message-ID: <move-away@example.test>\r\nSubject: Move away\r\n\r\nBody\r\n")
	source, fingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 63, raw,
		"<move-away@example.test>", "thread:move-away", now)
	transfer, err := db.StageMessageTransfer(ctx, user.ID, source.ID, archive.ID, "move", fingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, user.ID, transfer.ID, 64, archive.UIDValidity); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, source.ID, fingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 65, raw,
		"<move-away@example.test>", "thread:move-away", now.Add(time.Second))
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, now.Add(time.Second))
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("unrelated Inbox delivery decision=%+v err=%v", decision, err)
	}
}

func TestInboxArrivalDeliveryFinalizationIsConcurrentAndRecoverySafe(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-delivery@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 500)
	run, err := db.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 16, 0, 0, 0, time.UTC)
	raw := []byte("Message-ID: <delivery@example.test>\r\nSubject: Delivery\r\n\r\nNew\r\n")
	message, fingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 50,
		raw, "<delivery@example.test>", "thread:delivery", now)
	if _, err := db.SnoozeMessage(ctx, user.ID, message.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, run.ID, message, fingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("hold decision=%+v err=%v", decision, err)
	}
	due, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID)
	if err != nil || !due.Equal(now.Add(inboxArrivalHoldDuration)) {
		t.Fatalf("next due=%v err=%v", due, err)
	}
	schedules, err := db.ListPendingInboxArrivalSchedules(ctx)
	if err != nil || len(schedules) != 1 || schedules[0].UserID != user.ID || schedules[0].AccountID != account.ID || !schedules[0].DueAt.Equal(due) {
		t.Fatalf("schedules=%+v err=%v", schedules, err)
	}
	dueRows, err := db.ListDueInboxArrivals(ctx, user.ID, account.ID, due, 10)
	if err != nil || len(dueRows) != 1 || dueRows[0].MessageID != message.ID {
		t.Fatalf("due rows=%+v err=%v", dueRows, err)
	}

	start := make(chan struct{})
	results := make(chan struct {
		count int
		err   error
	}, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			count, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, due)
			results <- struct {
				count int
				err   error
			}{count, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		created += result.count
	}
	if created != 1 {
		t.Fatalf("concurrent finalizers created %d events, want 1", created)
	}
	if _, err := db.MessageSnoozeForUser(ctx, user.ID, message.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delivery snooze lookup error=%v, want not found", err)
	}
	savedRun, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil || savedRun.NewMessages != 1 || savedRun.LatestNewMessageID != message.ID {
		t.Fatalf("run after finalization=%+v err=%v", savedRun, err)
	}
	if err := db.UpdateSyncRunProgress(ctx, user.ID, run.ID, SyncProgress{}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSyncRun(ctx, user.ID, run.ID, "ok", SyncProgress{}, ""); err != nil {
		t.Fatal(err)
	}
	savedRun, err = db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil || savedRun.NewMessages != 1 || savedRun.LatestNewMessageID != message.ID {
		t.Fatalf("stale progress erased arrival=%+v err=%v", savedRun, err)
	}
	if _, err := db.NextPendingInboxArrivalDue(ctx, user.ID, account.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("next due after finalization error=%v, want not found", err)
	}

	if _, err := db.DB().ExecContext(ctx, `DELETE FROM new_mail_events WHERE user_id = ? AND message_id = ?`, user.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	recoveryRun, err := db.CreateSyncRun(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, recoveryRun.ID, message, fingerprint, due.Add(time.Second))
	if err != nil || !decision.EventCreated || decision.Event.MessageID != message.ID {
		t.Fatalf("delivery recovery decision=%+v err=%v", decision, err)
	}
	recoveredRun, err := db.GetSyncRunForUser(ctx, user.ID, recoveryRun.ID)
	if err != nil || recoveredRun.NewMessages != 1 {
		t.Fatalf("recovery run=%+v err=%v", recoveredRun, err)
	}
}

func TestReconcileRecordsGenerationScopedExpungeAtomically(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-expunge@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 700)
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 701)
	now := time.Date(2026, 7, 14, 17, 0, 0, 0, time.UTC)
	raw := []byte("Message-ID: <external@example.test>\r\nSubject: External\r\n\r\nMoved\r\n")
	source, sourceFingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 70,
		raw, "<external@example.test>", "thread:external", now)
	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 71,
		raw, "<external@example.test>", "thread:external", now)
	candidates, err := db.ListPotentialMoveSources(ctx, user.ID, arrival.ID, 20)
	if err != nil || len(candidates) != 1 || candidates[0].Message.ID != source.ID || candidates[0].SourceUIDValidity != spam.UIDValidity {
		t.Fatalf("potential sources=%+v err=%v", candidates, err)
	}
	withoutCutoff, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), 0,
		map[int64]string{source.ID: sourceFingerprint.CanonicalSHA256})
	if err != nil || len(withoutCutoff) != 0 {
		t.Fatalf("reconcile without UIDNEXT deleted=%+v err=%v, want safe no-op", withoutCutoff, err)
	}
	if _, err := db.GetMessageForUser(ctx, user.ID, source.ID); err != nil {
		t.Fatalf("reconcile without UIDNEXT removed source: %v", err)
	}
	deleted, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		spam.ID, nil, uint32(spam.UIDValidity), source.UID+1,
		map[int64]string{source.ID: sourceFingerprint.CanonicalSHA256})
	if err != nil || len(deleted) != 1 || deleted[0].ID != source.ID {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	var tombstones int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND account_id = ? AND source_mailbox_id = ?`, user.ID,
		account.ID, spam.ID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 1 {
		t.Fatalf("tombstones=%d, want 1", tombstones)
	}
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalExternalMove {
		t.Fatalf("external decision=%+v err=%v", decision, err)
	}
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM messages WHERE user_id = ? AND id = ?`, user.ID, arrival.ID); err != nil {
		t.Fatal(err)
	}
	var consumedAt int64
	var consumedMessageID sql.NullInt64
	if err := db.DB().QueryRowContext(ctx, `SELECT consumed_at, consumed_message_id
		FROM expunged_message_fingerprints WHERE user_id = ? AND source_mailbox_id = ? AND source_uid = ?`,
		user.ID, spam.ID, source.UID).Scan(&consumedAt, &consumedMessageID); err != nil {
		t.Fatal(err)
	}
	if consumedAt == 0 || consumedMessageID.Valid {
		t.Fatalf("consumed evidence reactivated: consumed_at=%d message_id=%v", consumedAt, consumedMessageID)
	}
	duplicate, duplicateFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 73,
		raw, "<external@example.test>", "thread:external", now.Add(time.Second))
	decision, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, duplicate, duplicateFingerprint, now.Add(time.Second))
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("duplicate after consumed message deletion decision=%+v err=%v", decision, err)
	}

	legacy, legacyFingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 72,
		[]byte("Message-ID: <legacy@example.test>\r\n\r\nLegacy\r\n"),
		"<legacy@example.test>", "thread:legacy", now.Add(time.Second))
	if _, err := db.DB().ExecContext(ctx, `UPDATE messages SET uid_validity = 0 WHERE user_id = ? AND id = ?`, user.ID, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID, spam.ID,
		nil, uint32(spam.UIDValidity), legacy.UID+1,
		map[int64]string{legacy.ID: legacyFingerprint.CanonicalSHA256}); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_uid = ?`, user.ID, legacy.UID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("legacy generation created %d tombstones", tombstones)
	}
}

func TestPotentialSourcesAndExpungedAmbiguityFailOpen(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-ambiguous@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	spam := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 801)
	archive := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 802)
	allMail, err := db.GetOrCreateMailboxWithRole(ctx, user.ID, account.ID, "Todos", "all")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, allMail.ID, 0, 0, 1, 803); err != nil {
		t.Fatal(err)
	}
	allMail, err = db.GetMailboxForUser(ctx, user.ID, allMail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if allMail.Role != "all" || allMail.Icon != "archive" {
		t.Fatalf("localized All Mail role/icon = %q/%q", allMail.Role, allMail.Icon)
	}
	duplicateAll, err := db.GetOrCreateMailboxWithRole(ctx, user.ID, account.ID, "Everything", "all")
	if err != nil {
		t.Fatal(err)
	}
	if duplicateAll.Role != "" {
		t.Fatalf("duplicate All Mail role persisted as %q", duplicateAll.Role)
	}
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 804)
	now := time.Date(2026, 7, 14, 18, 0, 0, 0, time.UTC)
	raw := []byte("Message-ID: <ambiguous@example.test>\r\nSubject: Same\r\n\r\nSame\r\n")
	first, firstFingerprint := arrivalTestMessage(t, ctx, db, user, account, spam, 80,
		raw, "<ambiguous@example.test>", "thread:ambiguous", now)
	second, secondFingerprint := arrivalTestMessage(t, ctx, db, user, account, archive, 81,
		raw, "<ambiguous@example.test>", "thread:ambiguous", now)
	allMailMessage, allMailFingerprint := arrivalTestMessage(t, ctx, db, user, account, allMail, 82,
		raw, "<ambiguous@example.test>", "thread:ambiguous", now)
	arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 83,
		raw, "<ambiguous@example.test>", "thread:ambiguous", now)
	candidates, err := db.ListPotentialMoveSources(ctx, user.ID, arrival.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("ambiguous exact sources=%+v, want both plausible candidates", candidates)
	}
	if _, err := db.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, user.ID, account.ID,
		allMail.ID, nil, uint32(allMail.UIDValidity), allMailMessage.UID+1,
		map[int64]string{allMailMessage.ID: allMailFingerprint.CanonicalSHA256}); err != nil {
		t.Fatal(err)
	}
	var allMailTombstones int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ?`, user.ID, allMail.ID).Scan(&allMailTombstones); err != nil {
		t.Fatal(err)
	}
	if allMailTombstones != 0 {
		t.Fatalf("All Mail reconciliation created %d move tombstones", allMailTombstones)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, first.ID, firstFingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, second.ID, secondFingerprint.CanonicalSHA256); err != nil {
		t.Fatal(err)
	}
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("ambiguous exact decision=%+v err=%v", decision, err)
	}
	created, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, now.Add(inboxArrivalHoldDuration))
	if err != nil || created != 1 {
		t.Fatalf("ambiguous finalization created=%d err=%v", created, err)
	}
}

func TestInboxArrivalCorrelationAndSchedulesAreTenantIsolated(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	owner := createPendingMoveTestUser(t, ctx, db, "arrival-owner@example.test")
	other := createPendingMoveTestUser(t, ctx, db, "arrival-other@example.test")
	ownerAccount := createPendingMoveTestAccount(t, ctx, db, owner, "primary")
	otherAccount := createPendingMoveTestAccount(t, ctx, db, other, "primary")
	ownerSpam := arrivalTestMailbox(t, ctx, db, owner, ownerAccount, "Spam", 1001)
	ownerInbox := arrivalTestMailbox(t, ctx, db, owner, ownerAccount, "INBOX", 1002)
	otherInbox := arrivalTestMailbox(t, ctx, db, other, otherAccount, "INBOX", 2002)
	now := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	raw := []byte("Message-ID: <tenant-shared@example.test>\r\nSubject: Shared\r\n\r\nIdentical\r\n")
	ownerSource, ownerSourceFingerprint := arrivalTestMessage(t, ctx, db, owner, ownerAccount,
		ownerSpam, 100, raw, "<tenant-shared@example.test>", "thread:tenant", now)
	transfer, err := db.StageMessageTransfer(ctx, owner.ID, ownerSource.ID, ownerInbox.ID,
		"move", ownerSourceFingerprint.CanonicalSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessageTransferSucceeded(ctx, owner.ID, transfer.ID, 101, ownerInbox.UIDValidity); err != nil {
		t.Fatal(err)
	}
	otherArrival, otherFingerprint := arrivalTestMessage(t, ctx, db, other, otherAccount,
		otherInbox, 101, raw, "<tenant-shared@example.test>", "thread:tenant", now)
	otherRun, err := db.CreateSyncRun(ctx, other.ID, otherAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := db.HoldOrClassifyInboxArrival(ctx, other.ID, otherRun.ID,
		otherArrival, otherFingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalPending {
		t.Fatalf("other tenant decision=%+v err=%v", decision, err)
	}
	if _, err := db.HoldOrClassifyInboxArrival(ctx, owner.ID, 0, otherArrival, otherFingerprint, now); err == nil {
		t.Fatal("cross-tenant arrival hold succeeded")
	}
	schedules, err := db.ListPendingInboxArrivalSchedules(ctx)
	if err != nil || len(schedules) != 1 || schedules[0].UserID != other.ID {
		t.Fatalf("tenant schedules=%+v err=%v", schedules, err)
	}
	ownerArrival, ownerFingerprint := arrivalTestMessage(t, ctx, db, owner, ownerAccount,
		ownerInbox, 101, raw, "<tenant-shared@example.test>", "thread:tenant", now)
	decision, err = db.HoldOrClassifyInboxArrival(ctx, owner.ID, 0, ownerArrival, ownerFingerprint, now)
	if err != nil || decision.Arrival.Classification != ArrivalLocalMove {
		t.Fatalf("owner decision=%+v err=%v", decision, err)
	}
	created, err := db.FinalizeDueInboxArrivals(ctx, other.ID, otherAccount.ID,
		now.Add(inboxArrivalHoldDuration))
	if err != nil || created != 1 {
		t.Fatalf("other finalization created=%d err=%v", created, err)
	}
	ownerEvents, ownerCount, _, err := db.NewMailEventsAfter(ctx, owner.ID, 0, 5)
	if err != nil || ownerCount != 0 || len(ownerEvents) != 0 {
		t.Fatalf("owner events=%+v count=%d err=%v", ownerEvents, ownerCount, err)
	}
	otherEvents, otherCount, _, err := db.NewMailEventsAfter(ctx, other.ID, 0, 5)
	if err != nil || otherCount != 1 || len(otherEvents) != 1 || otherEvents[0].MessageID != otherArrival.ID {
		t.Fatalf("other events=%+v count=%d err=%v", otherEvents, otherCount, err)
	}
	otherSavedRun, err := db.GetSyncRunForUser(ctx, other.ID, otherRun.ID)
	if err != nil || otherSavedRun.NewMessages != 1 {
		t.Fatalf("other run=%+v err=%v", otherSavedRun, err)
	}
	if _, err := db.GetSyncRunForUser(ctx, owner.ID, otherRun.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant run lookup error=%v", err)
	}
}

func TestExpungedCanonicalAndMessageIDAmbiguityFailOpen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourceRaw1 []byte
		sourceRaw2 []byte
		arrivalRaw []byte
	}{
		{
			name:       "canonical",
			sourceRaw1: []byte("Message-ID: <weak@example.test>\r\nSubject: Weak\r\n\r\nBody\r\n"),
			sourceRaw2: []byte("Message-ID: <weak@example.test>\nSubject: Weak\n\nBody\n"),
			arrivalRaw: []byte("Message-ID: <weak@example.test>\rSubject: Weak\r\rBody\r"),
		},
		{
			name:       "message-id",
			sourceRaw1: []byte("Message-ID: <weak@example.test>\n\nAAAA"),
			sourceRaw2: []byte("Message-ID: <weak@example.test>\n\nBBBB"),
			arrivalRaw: []byte("Message-ID: <weak@example.test>\n\nCCCC"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openArrivalTestStore(t)
			defer db.Close()
			user := createPendingMoveTestUser(t, ctx, db, "arrival-"+tc.name+"@example.test")
			account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
			firstMailbox := arrivalTestMailbox(t, ctx, db, user, account, "Spam", 1101)
			secondMailbox := arrivalTestMailbox(t, ctx, db, user, account, "Archive", 1102)
			inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 1103)
			now := time.Date(2026, 7, 14, 21, 0, 0, 0, time.UTC)
			first, firstFingerprint := arrivalTestMessage(t, ctx, db, user, account,
				firstMailbox, 111, tc.sourceRaw1, "<weak@example.test>", "thread:weak", now)
			second, secondFingerprint := arrivalTestMessage(t, ctx, db, user, account,
				secondMailbox, 112, tc.sourceRaw2, "<weak@example.test>", "thread:weak", now)
			arrival, arrivalFingerprint := arrivalTestMessage(t, ctx, db, user, account,
				inbox, 113, tc.arrivalRaw, "<weak@example.test>", "thread:weak", now)
			if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, first.ID, firstFingerprint.CanonicalSHA256); err != nil {
				t.Fatal(err)
			}
			if err := db.RecordExpungedMessageFingerprint(ctx, user.ID, second.ID, secondFingerprint.CanonicalSHA256); err != nil {
				t.Fatal(err)
			}
			decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, arrival, arrivalFingerprint, now)
			if err != nil || decision.Arrival.Classification != ArrivalPending {
				t.Fatalf("%s ambiguity decision=%+v err=%v", tc.name, decision, err)
			}
		})
	}
}

func TestFinalizedInboxArrivalRetentionIsBounded(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "arrival-retention@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	inbox := arrivalTestMailbox(t, ctx, db, user, account, "INBOX", 901)
	now := time.Date(2026, 7, 14, 19, 0, 0, 0, time.UTC)
	oldMessage, oldFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 90,
		[]byte("Message-ID: <old@example.test>\r\n\r\nOld\r\n"), "<old@example.test>", "thread:old", now)
	if _, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, oldMessage, oldFingerprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinalizeDueInboxArrivals(ctx, user.ID, account.ID, now.Add(inboxArrivalHoldDuration)); err != nil {
		t.Fatal(err)
	}
	oldFinalized := now.Add(-finalizedArrivalRetention - time.Second).Unix()
	if _, err := db.DB().ExecContext(ctx, `UPDATE pending_inbox_arrivals
		SET finalized_at = ?, updated_at = ? WHERE user_id = ? AND message_id = ?`,
		oldFinalized, oldFinalized, user.ID, oldMessage.ID); err != nil {
		t.Fatal(err)
	}
	newMessage, newFingerprint := arrivalTestMessage(t, ctx, db, user, account, inbox, 91,
		[]byte("Message-ID: <new@example.test>\r\n\r\nNew\r\n"), "<new@example.test>", "thread:new", now.Add(time.Minute))
	if _, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, newMessage, newFingerprint, now); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_inbox_arrivals
		WHERE user_id = ? AND message_id = ?`, user.ID, oldMessage.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 0 {
		t.Fatalf("expired finalized arrivals retained=%d", retained)
	}
}

func TestInboxArrivalCorrelationIsTenantIsolated(t *testing.T) {
	ctx := context.Background()
	db := openArrivalTestStore(t)
	defer db.Close()
	firstUser := createPendingMoveTestUser(t, ctx, db, "arrival-tenant-one@example.test")
	secondUser := createPendingMoveTestUser(t, ctx, db, "arrival-tenant-two@example.test")
	firstAccount := createPendingMoveTestAccount(t, ctx, db, firstUser, "primary")
	secondAccount := createPendingMoveTestAccount(t, ctx, db, secondUser, "primary")
	firstInbox := arrivalTestMailbox(t, ctx, db, firstUser, firstAccount, "INBOX", 1001)
	secondInbox := arrivalTestMailbox(t, ctx, db, secondUser, secondAccount, "INBOX", 1001)
	now := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	raw := []byte("Message-ID: <same-across-tenants@example.test>\r\nSubject: Same\r\n\r\nSame bytes\r\n")
	firstMessage, firstFingerprint := arrivalTestMessage(t, ctx, db, firstUser, firstAccount,
		firstInbox, 100, raw, "<same-across-tenants@example.test>", "thread:same", now)
	secondMessage, secondFingerprint := arrivalTestMessage(t, ctx, db, secondUser, secondAccount,
		secondInbox, 100, raw, "<same-across-tenants@example.test>", "thread:same", now)

	firstDecision, err := db.HoldOrClassifyInboxArrival(ctx, firstUser.ID, 0, firstMessage, firstFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	secondDecision, err := db.HoldOrClassifyInboxArrival(ctx, secondUser.ID, 0, secondMessage, secondFingerprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.HoldOrClassifyInboxArrival(ctx, firstUser.ID, 0, secondMessage, secondFingerprint, now); err == nil {
		t.Fatal("first tenant accepted the second tenant's message")
	}
	created, err := db.FinalizeDueInboxArrivals(ctx, firstUser.ID, firstAccount.ID, firstDecision.Arrival.AvailableAt)
	if err != nil || created != 1 {
		t.Fatalf("first tenant finalization created=%d err=%v", created, err)
	}
	firstEvents, firstCount, _, err := db.NewMailEventsAfter(ctx, firstUser.ID, 0, 5)
	if err != nil || firstCount != 1 || len(firstEvents) != 1 || firstEvents[0].MessageID != firstMessage.ID {
		t.Fatalf("first tenant events=%+v count=%d err=%v", firstEvents, firstCount, err)
	}
	secondEvents, secondCount, _, err := db.NewMailEventsAfter(ctx, secondUser.ID, 0, 5)
	if err != nil || secondCount != 0 || len(secondEvents) != 0 {
		t.Fatalf("second tenant changed by first finalizer events=%+v count=%d err=%v", secondEvents, secondCount, err)
	}
	if due, err := db.NextPendingInboxArrivalDue(ctx, secondUser.ID, secondAccount.ID); err != nil || !due.Equal(secondDecision.Arrival.AvailableAt) {
		t.Fatalf("second tenant pending deadline=%v err=%v", due, err)
	}
}

func openArrivalTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func arrivalTestMailbox(t *testing.T, ctx context.Context, db *Store, user User, account MailAccount, name string, uidValidity uint32) Mailbox {
	t.Helper()
	mailbox := createPendingMoveTestMailbox(t, ctx, db, user, account, name)
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 0, 0, 1, uidValidity); err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mailbox
}

func arrivalTestMessage(t *testing.T, ctx context.Context, db *Store, user User, account MailAccount,
	mailbox Mailbox, uid uint32, raw []byte, messageID, threadKey string, internalDate time.Time,
) (MessageRecord, ArrivalFingerprint) {
	t.Helper()
	fingerprint := MessageArrivalFingerprint(raw, messageID, internalDate, int64(len(raw)))
	path := fmt.Sprintf("users/%d/arrival-tests/accounts/%d/mailboxes/%d/uid-%d.eml",
		user.ID, account.ID, mailbox.ID, uid)
	blob, err := db.CreateBlob(ctx, BlobRecord{UserID: user.ID, Kind: "message", Path: path,
		SHA256: fingerprint.RawSHA256, Size: int64(len(raw))})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, CreateMessage{
		UserID: user.ID, AccountID: account.ID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: messageID, CanonicalSHA256: fingerprint.CanonicalSHA256,
		MessageIDHash: fingerprint.MessageIDHash, ThreadKey: threadKey,
		Subject: "Arrival test", FromAddr: "sender@example.test", Date: internalDate,
		InternalDate: internalDate, UID: uid, UIDValidity: mailbox.UIDValidity,
		Size: fingerprint.Size, BlobPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	return message, fingerprint
}
