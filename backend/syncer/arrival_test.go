package syncer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/store"
)

type arrivalProbeFetcher struct {
	Fetcher
	statusByMailbox map[string]MailboxStatus
	existsByUID     map[string]bool
	statusCalls     []string
	uidExistsCalls  []string
}

func (f *arrivalProbeFetcher) MailboxStatus(_ context.Context, _ store.MailAccount, mailbox string) (MailboxStatus, error) {
	key := strings.ToLower(strings.TrimSpace(mailbox))
	f.statusCalls = append(f.statusCalls, key)
	return f.statusByMailbox[key], nil
}

func (f *arrivalProbeFetcher) UIDExists(_ context.Context, _ store.MailAccount, mailbox string, uid uint32) (bool, error) {
	key := fmt.Sprintf("%s:%d", strings.ToLower(strings.TrimSpace(mailbox)), uid)
	f.uidExistsCalls = append(f.uidExistsCalls, key)
	return f.existsByUID[key], nil
}

type atomicArrivalProbeFetcher struct {
	*arrivalProbeFetcher
	uidValidityByMailbox map[string]uint32
	errByUID             map[string]error
}

func (f *atomicArrivalProbeFetcher) UIDExistsWithValidity(_ context.Context, _ store.MailAccount, mailbox string, uid uint32) (bool, uint32, error) {
	mailboxKey := strings.ToLower(strings.TrimSpace(mailbox))
	callKey := fmt.Sprintf("%s:%d", mailboxKey, uid)
	f.uidExistsCalls = append(f.uidExistsCalls, callKey)
	if err := f.errByUID[callKey]; err != nil {
		return false, f.uidValidityByMailbox[mailboxKey], err
	}
	return f.existsByUID[callKey], f.uidValidityByMailbox[mailboxKey], nil
}

type batchArrivalProbeCall struct {
	mailbox string
	uids    []uint32
}

type batchAtomicArrivalProbeFetcher struct {
	*atomicArrivalProbeFetcher
	batchCalls []batchArrivalProbeCall
}

func (f *batchAtomicArrivalProbeFetcher) ExistingUIDsWithValidity(_ context.Context, _ store.MailAccount, mailbox string, uids []uint32) ([]uint32, uint32, error) {
	mailboxKey := strings.ToLower(strings.TrimSpace(mailbox))
	f.batchCalls = append(f.batchCalls, batchArrivalProbeCall{mailbox: mailboxKey, uids: append([]uint32(nil), uids...)})
	existing := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		if f.existsByUID[fmt.Sprintf("%s:%d", mailboxKey, uid)] {
			existing = append(existing, uid)
		}
	}
	return existing, f.uidValidityByMailbox[mailboxKey], nil
}

func TestFinalizePendingInboxArrivalsRequiresPositiveMoveEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-classifier@example.test", "Arrival Classifier", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival", EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "INBOX", "inbox", 900)
	spam := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "Spam", "junk", 100)
	allMail := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "[Gmail]/All Mail", "", 200)
	archive := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "Archive", "", 300)
	oldGeneration := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "Old generation", "", 400)

	fetcher := &atomicArrivalProbeFetcher{
		arrivalProbeFetcher: &arrivalProbeFetcher{
			statusByMailbox: map[string]MailboxStatus{
				"spam":             {UIDValidity: 100},
				"[gmail]/all mail": {UIDValidity: 200},
				"archive":          {UIDValidity: 300},
				"old generation":   {UIDValidity: 401},
			},
			existsByUID: map[string]bool{"archive:30": true},
		},
		uidValidityByMailbox: map[string]uint32{
			"spam":           100,
			"archive":        300,
			"old generation": 401,
		},
	}
	service := &Service{Store: db, Fetcher: fetcher}
	base := time.Date(2026, time.July, 14, 18, 0, 0, 0, time.UTC)

	// An exact Spam source UID proven absent is a safe external-move signal.
	movedRaw := []byte("Message-ID: <moved@example.test>\r\nSubject: Moved\r\n\r\nbody")
	arrivalTestMessage(t, ctx, db, user.ID, account.ID, spam, 10, movedRaw, base)
	moved := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 101, movedRaw, base)
	movedDecision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, moved,
		store.MessageArrivalFingerprint(movedRaw, moved.MessageIDHeader, base, int64(len(movedRaw))), base)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, movedDecision.Arrival.AvailableAt)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("external move created %d delivery events, want 0", created)
	}

	// Gmail's All Mail duplicate is normal delivery topology, never move evidence.
	gmailRaw := []byte("Message-ID: <gmail@example.test>\r\nSubject: Gmail delivery\r\n\r\nbody")
	arrivalTestMessage(t, ctx, db, user.ID, account.ID, allMail, 20, gmailRaw, base.Add(time.Minute))
	gmail := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 102, gmailRaw, base.Add(time.Minute))
	gmailDecision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, gmail,
		store.MessageArrivalFingerprint(gmailRaw, gmail.MessageIDHeader, gmail.InternalDate, int64(len(gmailRaw))), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	created, _, err = service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, gmailDecision.Arrival.AvailableAt)
	if err != nil || created != 1 {
		t.Fatalf("Gmail delivery finalization created=%d err=%v, want one", created, err)
	}

	// A matching source that still exists is also a delivery, not a move.
	existingRaw := []byte("Message-ID: <existing@example.test>\r\nSubject: Existing source\r\n\r\nbody")
	arrivalTestMessage(t, ctx, db, user.ID, account.ID, archive, 30, existingRaw, base.Add(2*time.Minute))
	existing := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 103, existingRaw, base.Add(2*time.Minute))
	existingDecision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, existing,
		store.MessageArrivalFingerprint(existingRaw, existing.MessageIDHeader, existing.InternalDate, int64(len(existingRaw))), base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	created, _, err = service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, existingDecision.Arrival.AvailableAt)
	if err != nil || created != 1 {
		t.Fatalf("existing-source delivery finalization created=%d err=%v, want one", created, err)
	}

	// An absent UID from another UIDVALIDITY generation is not usable evidence.
	oldRaw := []byte("Message-ID: <old@example.test>\r\nSubject: Old generation\r\n\r\nbody")
	arrivalTestMessage(t, ctx, db, user.ID, account.ID, oldGeneration, 40, oldRaw, base.Add(3*time.Minute))
	oldArrival := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 104, oldRaw, base.Add(3*time.Minute))
	oldDecision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, oldArrival,
		store.MessageArrivalFingerprint(oldRaw, oldArrival.MessageIDHeader, oldArrival.InternalDate, int64(len(oldRaw))), base.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	created, nextDue, err := service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, oldDecision.Arrival.AvailableAt)
	if err != nil || created != 0 {
		t.Fatalf("UIDVALIDITY-mismatch finalization created=%d err=%v, want deferred", created, err)
	}
	if !nextDue.Equal(oldDecision.Arrival.AvailableAt.Add(inboxArrivalRetryDelay)) {
		t.Fatalf("UIDVALIDITY-mismatch retry=%v, want %v", nextDue,
			oldDecision.Arrival.AvailableAt.Add(inboxArrivalRetryDelay))
	}

	if got := strings.Join(fetcher.uidExistsCalls, ","); got != "spam:10,archive:30,old generation:40" {
		t.Fatalf("atomic UID existence probes = %q, want exact probes including the rejected UIDVALIDITY generation", got)
	}
	if len(fetcher.statusCalls) != 0 {
		t.Fatalf("atomic UID probes made separate mailbox status calls: %v", fetcher.statusCalls)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(events) != 2 {
		t.Fatalf("delivery events=%+v count=%d, want two proven deliveries", events, count)
	}
}

func TestFinalizePendingInboxArrivalsDoesNotFailOpenAfterMoveEvidencePersistenceError(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-persist-error@example.test", "Arrival Persist Error", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-persist-error", EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "INBOX", "inbox", 900)
	spam := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "Spam", "junk", 100)
	base := time.Date(2026, time.July, 14, 18, 30, 0, 0, time.UTC)
	raw := []byte("Message-ID: <persist-error@example.test>\r\nSubject: Moved\r\n\r\nbody")
	arrivalTestMessage(t, ctx, db, user.ID, account.ID, spam, 10, raw, base)
	inboxMessage := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 101, raw, base)
	decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, inboxMessage,
		store.MessageArrivalFingerprint(raw, inboxMessage.MessageIDHeader, base, int64(len(raw))), base)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &atomicArrivalProbeFetcher{
		arrivalProbeFetcher:  &arrivalProbeFetcher{existsByUID: map[string]bool{}},
		uidValidityByMailbox: map[string]uint32{"spam": 100},
	}
	service := &Service{Store: db, Fetcher: fetcher}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `CREATE TRIGGER fail_expunged_fingerprint_insert
		BEFORE INSERT ON expunged_message_fingerprints
		BEGIN SELECT RAISE(ABORT, 'injected expunged fingerprint failure'); END`); err != nil {
		t.Fatal(err)
	}
	created, _, err := service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt)
	if err == nil || !strings.Contains(err.Error(), "injected expunged fingerprint failure") {
		t.Fatalf("finalize error = %v, want injected persistence failure", err)
	}
	if created != 0 {
		t.Fatalf("failed finalization created %d delivery events, want 0", created)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(events) != 0 {
		t.Fatalf("failed finalization emitted events=%+v count=%d", events, count)
	}
	due, err := db.ListDueInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].MessageID != inboxMessage.ID || due[0].Classification != store.ArrivalPending {
		t.Fatalf("arrival after failed persistence = %+v, want original pending arrival", due)
	}
	if _, err := userDB.ExecContext(ctx, `DROP TRIGGER fail_expunged_fingerprint_insert`); err != nil {
		t.Fatal(err)
	}
	created, _, err = service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, decision.Arrival.AvailableAt)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("retried external move created %d delivery events, want 0", created)
	}
}

func TestFinalizePendingInboxArrivalsDefersOnlyUncertainPlausibleSources(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-probe-retry@example.test", "Arrival Probe Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-probe-retry", EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "INBOX", "inbox", 900)
	spam := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "Spam", "junk", 100)
	base := time.Date(2026, time.July, 14, 18, 45, 0, 0, time.UTC)
	uncertainRaw := []byte("Message-ID: <probe-retry@example.test>\r\nSubject: Probe retry\r\n\r\nbody")
	arrivalTestMessage(t, ctx, db, user.ID, account.ID, spam, 10, uncertainRaw, base)
	uncertainMessage := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 101, uncertainRaw, base)
	uncertainDecision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, uncertainMessage,
		store.MessageArrivalFingerprint(uncertainRaw, uncertainMessage.MessageIDHeader, base, int64(len(uncertainRaw))), base)
	if err != nil {
		t.Fatal(err)
	}
	safeRaw := []byte("Message-ID: <genuine-delivery@example.test>\r\nSubject: Genuine\r\n\r\nbody")
	safeMessage := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 102, safeRaw, base)
	_, err = db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, safeMessage,
		store.MessageArrivalFingerprint(safeRaw, safeMessage.MessageIDHeader, base, int64(len(safeRaw))), base)
	if err != nil {
		t.Fatal(err)
	}
	probeFailure := errors.New("injected UID existence failure")
	fetcher := &atomicArrivalProbeFetcher{
		arrivalProbeFetcher:  &arrivalProbeFetcher{existsByUID: map[string]bool{"spam:10": true}},
		uidValidityByMailbox: map[string]uint32{"spam": 100},
		errByUID:             map[string]error{"spam:10": probeFailure},
	}
	service := &Service{Store: db, Fetcher: fetcher}
	created, nextDue, err := service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID,
		uncertainDecision.Arrival.AvailableAt)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("mixed finalization created=%d, want genuine delivery only", created)
	}
	wantRetry := uncertainDecision.Arrival.AvailableAt.Add(inboxArrivalRetryDelay)
	if !nextDue.Equal(wantRetry) {
		t.Fatalf("uncertain retry=%v, want %v", nextDue, wantRetry)
	}
	events, count, _, err := db.NewMailEventsAfter(ctx, user.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(events) != 1 || events[0].MessageID != safeMessage.ID {
		t.Fatalf("events after uncertainty=%+v count=%d, want safe delivery only", events, count)
	}
	delete(fetcher.errByUID, "spam:10")
	created, _, err = service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, wantRetry)
	if err != nil || created != 1 {
		t.Fatalf("affirmative retry created=%d err=%v, want one delivery", created, err)
	}

	candidateRaw := []byte("Message-ID: <candidate-store-error@example.test>\r\nSubject: Candidate error\r\n\r\nbody")
	source := arrivalTestMessage(t, ctx, db, user.ID, account.ID, spam, 11, candidateRaw, base.Add(time.Minute))
	candidateMessage := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, 103, candidateRaw, base.Add(time.Minute))
	candidateDecision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, candidateMessage,
		store.MessageArrivalFingerprint(candidateRaw, candidateMessage.MessageIDHeader,
			candidateMessage.InternalDate, int64(len(candidateRaw))), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	userDB, err := db.UserDB(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE messages SET uid = 'invalid-uid'
		WHERE user_id = ? AND id = ?`, user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	created, nextDue, err = service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID,
		candidateDecision.Arrival.AvailableAt)
	if err != nil || created != 0 {
		t.Fatalf("candidate store-error finalization created=%d err=%v, want deferred", created, err)
	}
	wantCandidateRetry := candidateDecision.Arrival.AvailableAt.Add(inboxArrivalRetryDelay)
	if !nextDue.Equal(wantCandidateRetry) {
		t.Fatalf("candidate store-error retry=%v, want %v", nextDue, wantCandidateRetry)
	}
	if _, err := userDB.ExecContext(ctx, `UPDATE messages SET uid = 11
		WHERE user_id = ? AND id = ?`, user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	fetcher.existsByUID["spam:11"] = true
	created, _, err = service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, wantCandidateRetry)
	if err != nil || created != 1 {
		t.Fatalf("candidate store-error retry created=%d err=%v, want one delivery", created, err)
	}
}

func TestFinalizePendingInboxArrivalsProbesWholeFinalizationBatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "arrival-batch@example.test", "Arrival Batch", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: "arrival-batch", EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	inbox := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "INBOX", "inbox", 900)
	spam := arrivalTestMailbox(t, ctx, db, user.ID, account.ID, "Spam", "junk", 100)
	fetcher := &batchAtomicArrivalProbeFetcher{
		atomicArrivalProbeFetcher: &atomicArrivalProbeFetcher{
			arrivalProbeFetcher:  &arrivalProbeFetcher{existsByUID: map[string]bool{}},
			uidValidityByMailbox: map[string]uint32{"spam": 100},
		},
	}
	service := &Service{Store: db, Fetcher: fetcher}
	base := time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC)
	var due time.Time
	const arrivals = 21 // Deliberately exceeds the per-arrival source-candidate cap.
	for i := 0; i < arrivals; i++ {
		raw := []byte(fmt.Sprintf("Message-ID: <batch-%d@example.test>\r\nSubject: Batch %d\r\n\r\nbody-%d", i, i, i))
		internalDate := base.Add(time.Duration(i) * time.Second)
		arrivalTestMessage(t, ctx, db, user.ID, account.ID, spam, uint32(1000+i), raw, internalDate)
		inboxMessage := arrivalTestMessage(t, ctx, db, user.ID, account.ID, inbox, uint32(2000+i), raw, internalDate)
		decision, err := db.HoldOrClassifyInboxArrival(ctx, user.ID, 0, inboxMessage,
			store.MessageArrivalFingerprint(raw, inboxMessage.MessageIDHeader, internalDate, int64(len(raw))), base)
		if err != nil {
			t.Fatal(err)
		}
		due = decision.Arrival.AvailableAt
	}
	created, _, err := service.FinalizePendingInboxArrivals(ctx, user.ID, account.ID, due)
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("batch external moves created %d delivery events, want 0", created)
	}
	if len(fetcher.batchCalls) != 1 || fetcher.batchCalls[0].mailbox != "spam" || len(fetcher.batchCalls[0].uids) != arrivals {
		t.Fatalf("batch UID existence probes = %+v, want one Spam call with %d UIDs", fetcher.batchCalls, arrivals)
	}
	if len(fetcher.uidExistsCalls) != 0 {
		t.Fatalf("batch probe fell back to %d per-message UID calls", len(fetcher.uidExistsCalls))
	}
}

func arrivalTestMailbox(t *testing.T, ctx context.Context, db *store.Store, userID, accountID int64, name, role string, uidValidity uint32) store.Mailbox {
	t.Helper()
	mailbox, err := db.GetOrCreateMailboxWithRole(ctx, userID, accountID, name, role)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, userID, mailbox.ID, 0, 0, 1, uidValidity); err != nil {
		t.Fatal(err)
	}
	mailbox, err = db.GetMailboxForUser(ctx, userID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mailbox
}

func arrivalTestMessage(t *testing.T, ctx context.Context, db *store.Store, userID, accountID int64, mailbox store.Mailbox, uid uint32, raw []byte, internalDate time.Time) store.MessageRecord {
	t.Helper()
	fingerprint := store.MessageArrivalFingerprint(raw, messageIDFromArrivalTestRaw(raw), internalDate, int64(len(raw)))
	blob, err := db.CreateBlob(ctx, store.BlobRecord{
		UserID: userID, Kind: "message-remote",
		Path:   fmt.Sprintf("users/%d/arrival-test/%d/%d.eml", userID, mailbox.ID, uid),
		SHA256: fingerprint.RawSHA256, Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := db.CreateMessage(ctx, store.CreateMessage{
		UserID: userID, AccountID: accountID, MailboxID: mailbox.ID, BlobID: blob.ID,
		MessageIDHeader: messageIDFromArrivalTestRaw(raw), CanonicalSHA256: fingerprint.CanonicalSHA256,
		MessageIDHash: fingerprint.MessageIDHash, Subject: "Arrival test", FromAddr: "sender@example.test",
		Date: internalDate, InternalDate: internalDate, UID: uid, UIDValidity: mailbox.UIDValidity,
		Size: int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func messageIDFromArrivalTestRaw(raw []byte) string {
	first := strings.SplitN(string(raw), "\r\n", 2)[0]
	return strings.TrimSpace(strings.TrimPrefix(first, "Message-ID:"))
}
