// File overview: Sync-side ordering coverage for durable move/copy journals and expunge tombstones.

package syncer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

type receiptMoveTestFetcher struct {
	*moveTestFetcher
	mu                sync.Mutex
	receiptCalls      []moveTestCall
	receipt           *MoveReceipt
	err               error
	sourceExists      bool
	existenceValidity uint32
	existenceErr      error
	existenceCalls    int
	moveStarted       chan struct{}
	moveRelease       chan struct{}
}

func (f *receiptMoveTestFetcher) MoveMessageWithReceipt(_ context.Context, account store.MailAccount, source, destination string, uid uint32, _ uint32) (*MoveReceipt, error) {
	f.mu.Lock()
	f.receiptCalls = append(f.receiptCalls, moveTestCall{account: account, source: source, destination: destination, uid: uid})
	f.mu.Unlock()
	if f.moveStarted != nil {
		f.moveStarted <- struct{}{}
	}
	if f.moveRelease != nil {
		<-f.moveRelease
	}
	return f.receipt, f.err
}

func (f *receiptMoveTestFetcher) UIDExistsWithValidity(context.Context, store.MailAccount, string, uint32) (bool, uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.existenceCalls++
	validity := f.existenceValidity
	if validity == 0 {
		validity = moveTestSourceUIDValidity
	}
	return f.sourceExists, validity, f.existenceErr
}

type copyJournalTestFetcher struct {
	*moveTestFetcher
	mu               sync.Mutex
	raw              []byte
	fetchUIDValidity uint32
	destinationUID   uint32
	destinationValid uint32
	appendResult     FetchedMessage
	appendErr        error
	appendCalls      int
	boundary         MailboxAppendBoundary
	boundaryErr      error
	boundaryCalls    int
	exactSnapshot    ExactMessageMatchSnapshot
	exactErr         error
	exactCalls       int
	exactMinimumUIDs []uint32
	appendStarted    chan struct{}
	appendRelease    chan struct{}
}

func (f *copyJournalTestFetcher) FetchMessage(_ context.Context, _ store.MailAccount, mailbox string, uid uint32) (FetchedMessage, error) {
	uidValidity := f.fetchUIDValidity
	if uid == f.destinationUID && f.destinationValid > 0 {
		uidValidity = f.destinationValid
	}
	if uidValidity == 0 {
		uidValidity = moveTestSourceUIDValidity
	}
	return FetchedMessage{Mailbox: mailbox, UID: uid, UIDValidity: uidValidity, Raw: f.raw, Size: int64(len(f.raw))}, nil
}

func (f *copyJournalTestFetcher) AppendMessage(_ context.Context, _ store.MailAccount, _ string, _ []byte, _ string, _ time.Time) (FetchedMessage, error) {
	f.mu.Lock()
	f.appendCalls++
	f.mu.Unlock()
	if f.appendStarted != nil {
		f.appendStarted <- struct{}{}
	}
	if f.appendRelease != nil {
		<-f.appendRelease
	}
	return f.appendResult, f.appendErr
}

func (f *copyJournalTestFetcher) SnapshotMailboxAppendBoundary(context.Context, store.MailAccount, string) (MailboxAppendBoundary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boundaryCalls++
	boundary := f.boundary
	if boundary.UIDValidity == 0 {
		boundary = MailboxAppendBoundary{UIDValidity: 701, UIDNext: 1}
	}
	return boundary, f.boundaryErr
}

func (f *copyJournalTestFetcher) SnapshotExactMessageMatches(_ context.Context, _ store.MailAccount, _ string, _ string, _ []byte, minimumUID uint32) (ExactMessageMatchSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exactCalls++
	f.exactMinimumUIDs = append(f.exactMinimumUIDs, minimumUID)
	snapshot := f.exactSnapshot
	if snapshot.UIDValidity == 0 {
		snapshot.UIDValidity = 701
	}
	if snapshot.UIDNext == 0 {
		snapshot.UIDNext = 1
	}
	return snapshot, f.exactErr
}

type reconcileJournalTestFetcher struct {
	*moveTestFetcher
	uids          []uint32
	uidValidity   uint32
	uidNext       uint32
	afterSnapshot func()
	snapshotCalls int
	legacyCalls   int
}

func (f *reconcileJournalTestFetcher) UIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	f.legacyCalls++
	return append([]uint32(nil), f.uids...), nil
}

func (f *reconcileJournalTestFetcher) SnapshotMailboxUIDs(context.Context, store.MailAccount, string) (MailboxUIDSnapshot, error) {
	f.snapshotCalls++
	if f.afterSnapshot != nil {
		f.afterSnapshot()
	}
	return MailboxUIDSnapshot{
		UIDs: append([]uint32(nil), f.uids...), UIDValidity: f.uidValidity, UIDNext: f.uidNext,
	}, nil
}

func transferState(t *testing.T, fixture moveTestFixture) (string, string, uint32, int64) {
	t.Helper()
	var kind, state string
	var destinationUID uint32
	var destinationUIDValidity int64
	if err := fixture.store.DB().QueryRow(`SELECT operation_kind, state, destination_uid, destination_uid_validity
		FROM message_transfers WHERE user_id = ? ORDER BY id DESC LIMIT 1`, fixture.userID).
		Scan(&kind, &state, &destinationUID, &destinationUIDValidity); err != nil {
		t.Fatal(err)
	}
	return kind, state, destinationUID, destinationUIDValidity
}

func TestMoveMessageRecordsCOPYUIDBeforeLocalCleanup(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &receiptMoveTestFetcher{
		moveTestFetcher: fixture.fetcher,
		receipt:         &MoveReceipt{DestinationUIDValidity: 901, DestinationUID: 73},
	}
	fixture.service.Fetcher = fetcher

	if err := fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatal(err)
	}
	if len(fetcher.receiptCalls) != 1 || len(fetcher.moveCalls) != 0 {
		t.Fatalf("receipt calls=%d fallback calls=%d, want 1 and 0", len(fetcher.receiptCalls), len(fetcher.moveCalls))
	}
	kind, state, uid, uidValidity := transferState(t, fixture)
	if kind != "move" || state != "consumed" || uid != 73 || uidValidity != 901 {
		t.Fatalf("transfer kind=%q state=%q uid=%d uidvalidity=%d", kind, state, uid, uidValidity)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); !store.IsNotFound(err) {
		t.Fatalf("source message still present after MOVE: %v", err)
	}
}

func TestMoveMessageCleanupFailureRetriesConsumedTransferWithoutRedispatch(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &receiptMoveTestFetcher{
		moveTestFetcher: fixture.fetcher,
		receipt:         &MoveReceipt{DestinationUIDValidity: 901, DestinationUID: 74},
	}
	fixture.service.Fetcher = fetcher
	ctx := context.Background()
	if _, err := fixture.store.DB().ExecContext(ctx, `CREATE TRIGGER fail_moved_message_cleanup
		BEFORE DELETE ON messages BEGIN
			SELECT RAISE(ABORT, 'injected moved message cleanup failure');
		END`); err != nil {
		t.Fatal(err)
	}

	err := fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "injected moved message cleanup failure") {
		t.Fatalf("cleanup failure error=%v", err)
	}
	if len(fetcher.receiptCalls) != 1 {
		t.Fatalf("initial remote MOVE calls=%d, want 1", len(fetcher.receiptCalls))
	}
	_, state, uid, uidValidity := transferState(t, fixture)
	if state != "consumed" || uid != 74 || uidValidity != 901 {
		t.Fatalf("cleanup failure transfer state=%q uid=%d uidvalidity=%d", state, uid, uidValidity)
	}
	if _, err := fixture.store.GetMessageForUser(ctx, fixture.userID, fixture.message.ID); err != nil {
		t.Fatalf("cleanup failure removed retry source: %v", err)
	}
	if _, err := fixture.store.DB().ExecContext(ctx, `DROP TRIGGER fail_moved_message_cleanup`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatalf("consumed cleanup retry: %v", err)
	}
	if len(fetcher.receiptCalls) != 1 {
		t.Fatalf("cleanup retry remote MOVE calls=%d, want 1", len(fetcher.receiptCalls))
	}
	if _, err := fixture.store.GetMessageForUser(ctx, fixture.userID, fixture.message.ID); !store.IsNotFound(err) {
		t.Fatalf("cleanup retry source still present: %v", err)
	}
	var transferCount int
	if err := fixture.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_transfers
		WHERE user_id = ?`, fixture.userID).Scan(&transferCount); err != nil {
		t.Fatal(err)
	}
	if transferCount != 1 {
		t.Fatalf("cleanup retry transfer rows=%d, want 1", transferCount)
	}
}

func TestMoveMessageTerminalizationFailureLeavesLocalSourceIntact(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &receiptMoveTestFetcher{
		moveTestFetcher: fixture.fetcher,
		receipt:         &MoveReceipt{DestinationUIDValidity: 901, DestinationUID: 75},
	}
	fixture.service.Fetcher = fetcher
	ctx := context.Background()
	if _, err := fixture.store.DB().ExecContext(ctx, `CREATE TRIGGER fail_move_terminalization
		BEFORE UPDATE OF state ON message_transfers WHEN NEW.state = 'consumed' BEGIN
			SELECT RAISE(ABORT, 'injected move terminalization failure');
		END`); err != nil {
		t.Fatal(err)
	}

	err := fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "injected move terminalization failure") {
		t.Fatalf("terminalization failure error=%v", err)
	}
	if len(fetcher.receiptCalls) != 1 {
		t.Fatalf("terminalization failure remote MOVE calls=%d, want 1", len(fetcher.receiptCalls))
	}
	_, state, uid, uidValidity := transferState(t, fixture)
	if state != "succeeded" || uid != 75 || uidValidity != 901 {
		t.Fatalf("terminalization failure transfer state=%q uid=%d uidvalidity=%d", state, uid, uidValidity)
	}
	if _, err := fixture.store.GetMessageForUser(ctx, fixture.userID, fixture.message.ID); err != nil {
		t.Fatalf("terminalization failure removed local source: %v", err)
	}
}

func TestMoveMessageKeepsUnknownOutcomePending(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("connection lost after command write")
	fixture.fetcher.moveErr = MoveOutcomeUnknown(remoteErr)

	err := fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("MoveMessage error = %v, want %v", err, remoteErr)
	}
	kind, state, _, _ := transferState(t, fixture)
	if kind != "move" || state != "pending" {
		t.Fatalf("transfer kind=%q state=%q, want move/pending", kind, state)
	}
	if retryErr := fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID); retryErr == nil {
		t.Fatalf("pending retry error = %v", retryErr)
	}
	if len(fixture.fetcher.moveCalls) != 1 {
		t.Fatalf("pending retry MOVE calls=%d, want 1", len(fixture.fetcher.moveCalls))
	}
}

func TestMoveMessageRecoversCrashBeforeRemoteCommand(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	transfer, err := fixture.store.StageMessageTransfer(ctx, fixture.userID, fixture.message.ID,
		fixture.destination.ID, "move", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := fixture.store.ClaimMessageTransferDispatchForOwner(ctx, fixture.userID,
		transfer.ID, "previous-process"); err != nil || !claimed {
		t.Fatalf("simulate prior claim claimed=%v err=%v", claimed, err)
	}
	remoteErr := errors.New("stop after recovered dispatch")
	fetcher := &receiptMoveTestFetcher{
		moveTestFetcher: fixture.fetcher,
		sourceExists:    true,
		err:             MoveOutcomeUnknown(remoteErr),
	}
	fixture.service.Fetcher = fetcher

	err = fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("recovered move error=%v, want %v", err, remoteErr)
	}
	if len(fetcher.receiptCalls) != 1 || fetcher.existenceCalls != 1 {
		t.Fatalf("recovered move calls=%d existence=%d, want 1/1", len(fetcher.receiptCalls), fetcher.existenceCalls)
	}
	var attempt int64
	if err := fixture.store.DB().QueryRow(`SELECT dispatch_attempt FROM message_transfers
		WHERE user_id = ? AND id = ?`, fixture.userID, transfer.ID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if attempt != 2 {
		t.Fatalf("recovered dispatch attempt=%d, want 2", attempt)
	}
}

func TestMoveMessageReconcilesAppliedUnknownOutcomeWithoutResend(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("connection lost after MOVE")
	fetcher := &receiptMoveTestFetcher{moveTestFetcher: fixture.fetcher, err: MoveOutcomeUnknown(remoteErr)}
	fixture.service.Fetcher = fetcher
	ctx := context.Background()

	if err := fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); !errors.Is(err, remoteErr) {
		t.Fatalf("initial move error=%v, want %v", err, remoteErr)
	}
	fetcher.err = nil
	fetcher.sourceExists = false
	if err := fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatalf("reconcile applied move: %v", err)
	}
	if len(fetcher.receiptCalls) != 1 || fetcher.existenceCalls != 1 {
		t.Fatalf("applied move calls=%d existence=%d, want 1/1", len(fetcher.receiptCalls), fetcher.existenceCalls)
	}
	_, state, _, _ := transferState(t, fixture)
	if state != "consumed" {
		t.Fatalf("applied move transfer state=%q, want consumed", state)
	}
}

func TestMoveMessageConcurrentCallerCannotReopenActiveDispatch(t *testing.T) {
	fixture := newMoveTestFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fetcher := &receiptMoveTestFetcher{
		moveTestFetcher: fixture.fetcher,
		moveStarted:     started,
		moveRelease:     release,
	}
	fixture.service.Fetcher = fetcher
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	}()
	<-started
	secondErr := fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if secondErr == nil || !strings.Contains(secondErr.Error(), "awaiting remote reconciliation") {
		t.Fatalf("concurrent move error=%v", secondErr)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first move: %v", err)
	}
	if len(fetcher.receiptCalls) != 1 || fetcher.existenceCalls != 0 {
		t.Fatalf("concurrent move calls=%d existence=%d, want 1/0", len(fetcher.receiptCalls), fetcher.existenceCalls)
	}
}

func TestMoveMessageFinishesClaimWhenSuccessPersistenceFails(t *testing.T) {
	fixture := newMoveTestFixture(t)
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER fail_move_transfer_success
		BEFORE UPDATE OF state ON message_transfers
		WHEN NEW.state = 'succeeded'
		BEGIN SELECT RAISE(FAIL, 'forced transfer success failure'); END`); err != nil {
		t.Fatal(err)
	}
	fetcher := &receiptMoveTestFetcher{
		moveTestFetcher: fixture.fetcher,
		receipt:         &MoveReceipt{DestinationUIDValidity: 701, DestinationUID: 73},
	}
	fixture.service.Fetcher = fetcher
	err := fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "forced transfer success failure") {
		t.Fatalf("move persistence error=%v", err)
	}
	var state string
	var finished int64
	if err := fixture.store.DB().QueryRow(`SELECT state, dispatch_finished_at FROM message_transfers
		WHERE user_id = ? ORDER BY id DESC LIMIT 1`, fixture.userID).Scan(&state, &finished); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || finished == 0 {
		t.Fatalf("move persistence state=%q finished=%d, want pending/nonzero", state, finished)
	}
}

func TestMoveMessageDispatchesExistingUnclaimedTransfer(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	transfer, err := fixture.store.StageMessageTransfer(ctx, fixture.userID, fixture.message.ID,
		fixture.destination.ID, "move", "")
	if err != nil {
		t.Fatal(err)
	}
	if !transfer.DispatchedAt.IsZero() {
		t.Fatalf("staged transfer dispatched_at=%v, want zero", transfer.DispatchedAt)
	}
	remoteErr := errors.New("connection lost after resumed command write")
	fixture.fetcher.moveErr = MoveOutcomeUnknown(remoteErr)

	err = fixture.service.MoveMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("MoveMessage error = %v, want %v", err, remoteErr)
	}
	if len(fixture.fetcher.moveCalls) != 1 {
		t.Fatalf("remote move calls=%d, want 1", len(fixture.fetcher.moveCalls))
	}
	var dispatchedAt int64
	if err := fixture.store.DB().QueryRow(`SELECT dispatched_at FROM message_transfers
		WHERE user_id = ? AND id = ?`, fixture.userID, transfer.ID).Scan(&dispatchedAt); err != nil {
		t.Fatal(err)
	}
	if dispatchedAt == 0 {
		t.Fatal("resumed move did not persist its dispatch claim")
	}
}

func TestMoveMessageMarksDefinitiveFailure(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("permission denied")
	fixture.fetcher.moveErr = remoteErr

	err := fixture.service.MoveMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("MoveMessage error = %v, want %v", err, remoteErr)
	}
	kind, state, _, _ := transferState(t, fixture)
	if kind != "move" || state != "failed" {
		t.Fatalf("transfer kind=%q state=%q, want move/failed", kind, state)
	}
}

func TestCopyMessageRecordsRemoteSuccessBeforeUIDValidation(t *testing.T) {
	fixture := newMoveTestFixture(t)
	raw := []byte("Message-ID: <copy-journal@example.test>\r\nSubject: copy\r\n\r\nbody\r\n")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             raw,
		appendResult:    FetchedMessage{Mailbox: fixture.destination.Name},
	}
	fixture.service.Fetcher = fetcher

	err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "missing a UID") {
		t.Fatalf("CopyMessage error = %v, want missing UID", err)
	}
	if fetcher.appendCalls != 1 {
		t.Fatalf("append calls = %d, want 1", fetcher.appendCalls)
	}
	kind, state, uid, _ := transferState(t, fixture)
	if kind != "copy" || state != "succeeded" || uid != 0 {
		t.Fatalf("transfer kind=%q state=%q uid=%d, want copy/succeeded/0", kind, state, uid)
	}
	var canonical string
	if err := fixture.store.DB().QueryRow(`SELECT canonical_sha256 FROM message_transfers
		WHERE user_id = ? ORDER BY id DESC LIMIT 1`, fixture.userID).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	if canonical != store.CanonicalMessageSHA256(raw) {
		t.Fatalf("canonical fingerprint = %q", canonical)
	}
}

func TestCopyMessageMarksAppendFailure(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("append rejected")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <failed-copy@example.test>\r\n\r\nbody\r\n"),
		appendErr:       remoteErr,
	}
	fixture.service.Fetcher = fetcher

	err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("CopyMessage error = %v, want %v", err, remoteErr)
	}
	kind, state, _, _ := transferState(t, fixture)
	if kind != "copy" || state != "failed" {
		t.Fatalf("transfer kind=%q state=%q, want copy/failed", kind, state)
	}
}

func TestCopyMessageKeepsUnknownAppendOutcomePending(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("connection lost during append")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <unknown-copy@example.test>\r\n\r\nbody\r\n"),
		appendErr:       AppendOutcomeUnknown(remoteErr),
	}
	fixture.service.Fetcher = fetcher

	err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("CopyMessage error = %v, want %v", err, remoteErr)
	}
	kind, state, _, _ := transferState(t, fixture)
	if kind != "copy" || state != "pending" {
		t.Fatalf("transfer kind=%q state=%q, want copy/pending", kind, state)
	}
	if retryErr := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID); !errors.Is(retryErr, remoteErr) {
		t.Fatalf("pending retry error = %v", retryErr)
	}
	if fetcher.appendCalls != 2 || fetcher.exactCalls != 1 {
		t.Fatalf("proven-absent retry APPEND calls=%d exact=%d, want 2/1", fetcher.appendCalls, fetcher.exactCalls)
	}
}

func TestCopyMessageRecoversCrashBeforeRemoteCommand(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	raw := []byte("Message-ID: <copy-crash@example.test>\r\n\r\nbody\r\n")
	transfer, err := fixture.store.StageMessageTransfer(ctx, fixture.userID, fixture.message.ID,
		fixture.destination.ID, "copy", store.CanonicalMessageSHA256(raw))
	if err != nil {
		t.Fatal(err)
	}
	transfer, err = fixture.store.SetMessageTransferDestinationSnapshot(ctx, fixture.userID, transfer.ID, 701, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := fixture.store.ClaimMessageTransferDispatchForOwner(ctx, fixture.userID,
		transfer.ID, "previous-process"); err != nil || !claimed {
		t.Fatalf("simulate prior claim claimed=%v err=%v", claimed, err)
	}
	remoteErr := errors.New("stop after recovered APPEND")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             raw,
		appendErr:       AppendOutcomeUnknown(remoteErr),
	}
	fixture.service.Fetcher = fetcher

	err = fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("recovered copy error=%v, want %v", err, remoteErr)
	}
	if fetcher.appendCalls != 1 || fetcher.exactCalls != 1 {
		t.Fatalf("recovered copy append=%d exact=%d, want 1/1", fetcher.appendCalls, fetcher.exactCalls)
	}
	var attempt int64
	if err := fixture.store.DB().QueryRow(`SELECT dispatch_attempt FROM message_transfers
		WHERE user_id = ? AND id = ?`, fixture.userID, transfer.ID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if attempt != 2 {
		t.Fatalf("recovered copy attempt=%d, want 2", attempt)
	}
}

func TestCopyMessageReconcilesAppliedUnknownOutcomeWithoutResend(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	remoteErr := errors.New("connection lost after APPEND")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher:  fixture.fetcher,
		raw:              []byte("Message-ID: <copy-applied-unknown@example.test>\r\n\r\nbody\r\n"),
		appendErr:        AppendOutcomeUnknown(remoteErr),
		destinationUID:   1,
		destinationValid: 701,
	}
	fixture.service.Fetcher = fetcher
	ctx := context.Background()
	if err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); !errors.Is(err, remoteErr) {
		t.Fatalf("initial copy error=%v, want %v", err, remoteErr)
	}
	fetcher.exactSnapshot = ExactMessageMatchSnapshot{UIDValidity: 701, UIDNext: 2, MatchingUIDs: []uint32{1}}
	if err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatalf("reconcile applied copy: %v", err)
	}
	if fetcher.appendCalls != 1 || fetcher.exactCalls != 1 {
		t.Fatalf("applied copy append=%d exact=%d, want 1/1", fetcher.appendCalls, fetcher.exactCalls)
	}
	_, state, uid, validity := transferState(t, fixture)
	if state != "consumed" || uid != 1 || validity != 701 {
		t.Fatalf("applied copy state=%q uid=%d validity=%d", state, uid, validity)
	}
}

func TestCopyMessageReconcilesAppliedUnknownOutcomeWithoutMessageID(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	ctx := context.Background()
	if _, err := fixture.store.DB().ExecContext(ctx, `UPDATE messages
		SET message_id_header = '', message_id_hash = '' WHERE user_id = ? AND id = ?`,
		fixture.userID, fixture.message.ID); err != nil {
		t.Fatal(err)
	}
	remoteErr := errors.New("connection lost after Message-ID-less APPEND")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher:  fixture.fetcher,
		raw:              []byte("From: sender@example.test\r\nSubject: no id\r\n\r\nbody\r\n"),
		appendErr:        AppendOutcomeUnknown(remoteErr),
		boundary:         MailboxAppendBoundary{UIDValidity: 701, UIDNext: 44},
		destinationUID:   44,
		destinationValid: 701,
	}
	fixture.service.Fetcher = fetcher
	if err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); !errors.Is(err, remoteErr) {
		t.Fatalf("initial copy error=%v, want %v", err, remoteErr)
	}
	fetcher.exactSnapshot = ExactMessageMatchSnapshot{UIDValidity: 701, UIDNext: 45, MatchingUIDs: []uint32{44}, CandidateUIDs: []uint32{44}}
	if err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatalf("reconcile Message-ID-less copy: %v", err)
	}
	if fetcher.appendCalls != 1 || fetcher.exactCalls != 1 {
		t.Fatalf("Message-ID-less copy append=%d exact=%d, want 1/1", fetcher.appendCalls, fetcher.exactCalls)
	}
	if len(fetcher.exactMinimumUIDs) != 1 || fetcher.exactMinimumUIDs[0] != 44 {
		t.Fatalf("exact minimum UIDs=%v, want [44]", fetcher.exactMinimumUIDs)
	}
	_, state, uid, validity := transferState(t, fixture)
	if state != "consumed" || uid != 44 || validity != 701 {
		t.Fatalf("Message-ID-less copy state=%q uid=%d validity=%d", state, uid, validity)
	}
}

func TestCopyMessagePreexistingExactMatchRemainsBlocked(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("connection lost after APPEND")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <copy-ambiguous@example.test>\r\n\r\nbody\r\n"),
		appendErr:       AppendOutcomeUnknown(remoteErr),
		boundary:        MailboxAppendBoundary{UIDValidity: 701, UIDNext: 10},
	}
	fixture.service.Fetcher = fetcher
	ctx := context.Background()
	if err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); !errors.Is(err, remoteErr) {
		t.Fatalf("initial copy error=%v, want %v", err, remoteErr)
	}
	fetcher.exactSnapshot = ExactMessageMatchSnapshot{UIDValidity: 701, UIDNext: 11, MatchingUIDs: []uint32{5}}
	err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous copy error=%v", err)
	}
	if fetcher.appendCalls != 1 {
		t.Fatalf("ambiguous copy APPEND calls=%d, want 1", fetcher.appendCalls)
	}
}

func TestCopyMessagePostBoundaryNonExactCandidateRemainsBlocked(t *testing.T) {
	fixture := newMoveTestFixture(t)
	remoteErr := errors.New("connection lost after APPEND")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <copy-rewritten@example.test>\r\n\r\nbody\r\n"),
		appendErr:       AppendOutcomeUnknown(remoteErr),
	}
	fixture.service.Fetcher = fetcher
	ctx := context.Background()
	if err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID); !errors.Is(err, remoteErr) {
		t.Fatalf("initial copy error=%v, want %v", err, remoteErr)
	}
	fetcher.exactSnapshot = ExactMessageMatchSnapshot{UIDValidity: 701, UIDNext: 2, CandidateUIDs: []uint32{1}}
	err := fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "not an exact raw match") {
		t.Fatalf("rewritten candidate error=%v", err)
	}
	if fetcher.appendCalls != 1 {
		t.Fatalf("rewritten candidate APPEND calls=%d, want 1", fetcher.appendCalls)
	}
}

func TestCopyMessageConcurrentCallerCannotReopenActiveDispatch(t *testing.T) {
	fixture := newMoveTestFixture(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	remoteErr := errors.New("append rejected")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <copy-concurrent@example.test>\r\n\r\nbody\r\n"),
		appendErr:       remoteErr,
		appendStarted:   started,
		appendRelease:   release,
	}
	fixture.service.Fetcher = fetcher
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	}()
	<-started
	secondErr := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if secondErr == nil || !strings.Contains(secondErr.Error(), "awaiting remote reconciliation") {
		t.Fatalf("concurrent copy error=%v", secondErr)
	}
	close(release)
	if err := <-firstDone; !errors.Is(err, remoteErr) {
		t.Fatalf("first copy error=%v, want %v", err, remoteErr)
	}
	if fetcher.appendCalls != 1 || fetcher.exactCalls != 0 {
		t.Fatalf("concurrent copy append=%d exact=%d, want 1/0", fetcher.appendCalls, fetcher.exactCalls)
	}
}

func TestCopyMessageFinishesClaimWhenSuccessPersistenceFails(t *testing.T) {
	fixture := newMoveTestFixture(t)
	if _, err := fixture.store.DB().Exec(`CREATE TRIGGER fail_copy_transfer_success
		BEFORE UPDATE OF state ON message_transfers
		WHEN NEW.state = 'succeeded'
		BEGIN SELECT RAISE(FAIL, 'forced transfer success failure'); END`); err != nil {
		t.Fatal(err)
	}
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <copy-persistence@example.test>\r\n\r\nbody\r\n"),
		appendResult: FetchedMessage{
			UID: 73, UIDValidity: 701, AppendUIDAuthoritative: true,
		},
	}
	fixture.service.Fetcher = fetcher
	err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if err == nil || !strings.Contains(err.Error(), "forced transfer success failure") {
		t.Fatalf("copy persistence error=%v", err)
	}
	var state string
	var finished int64
	if err := fixture.store.DB().QueryRow(`SELECT state, dispatch_finished_at FROM message_transfers
		WHERE user_id = ? ORDER BY id DESC LIMIT 1`, fixture.userID).Scan(&state, &finished); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || finished == 0 {
		t.Fatalf("copy persistence state=%q finished=%d, want pending/nonzero", state, finished)
	}
}

func TestCopyMessageDispatchesExistingUnclaimedTransfer(t *testing.T) {
	fixture := newMoveTestFixture(t)
	ctx := context.Background()
	raw := []byte("Message-ID: <resumed-copy@example.test>\r\n\r\nbody\r\n")
	transfer, err := fixture.store.StageMessageTransfer(ctx, fixture.userID, fixture.message.ID,
		fixture.destination.ID, "copy", store.CanonicalMessageSHA256(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !transfer.DispatchedAt.IsZero() {
		t.Fatalf("staged transfer dispatched_at=%v, want zero", transfer.DispatchedAt)
	}
	remoteErr := errors.New("connection lost during resumed append")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             raw,
		appendErr:       AppendOutcomeUnknown(remoteErr),
	}
	fixture.service.Fetcher = fetcher

	err = fixture.service.CopyMessage(ctx, fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("CopyMessage error = %v, want %v", err, remoteErr)
	}
	if fetcher.appendCalls != 1 {
		t.Fatalf("remote APPEND calls=%d, want 1", fetcher.appendCalls)
	}
	var dispatchedAt int64
	if err := fixture.store.DB().QueryRow(`SELECT dispatched_at FROM message_transfers
		WHERE user_id = ? AND id = ?`, fixture.userID, transfer.ID).Scan(&dispatchedAt); err != nil {
		t.Fatal(err)
	}
	if dispatchedAt == 0 {
		t.Fatal("resumed copy did not persist its dispatch claim")
	}
}

func TestCopyMessageRetainsAppliedAppendWithoutUID(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	remoteErr := errors.New("UID lookup failed after append")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             []byte("Message-ID: <applied-copy@example.test>\r\n\r\nbody\r\n"),
		appendErr:       AppendApplied(remoteErr),
	}
	fixture.service.Fetcher = fetcher

	err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID)
	if !errors.Is(err, remoteErr) {
		t.Fatalf("CopyMessage error = %v, want %v", err, remoteErr)
	}
	kind, state, uid, uidValidity := transferState(t, fixture)
	if kind != "copy" || state != "succeeded" || uid != 0 || uidValidity != 0 {
		t.Fatalf("transfer kind=%q state=%q uid=%d uidvalidity=%d, want copy/succeeded/0/0", kind, state, uid, uidValidity)
	}
	fetcher.exactSnapshot = ExactMessageMatchSnapshot{
		UIDValidity: 701,
		UIDNext:     2,
		MatchingUIDs: []uint32{
			1,
		},
	}
	fetcher.destinationUID = 1
	fetcher.destinationValid = 701
	if err := fixture.service.CopyMessage(context.Background(), fixture.userID, fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatalf("applied APPEND retry: %v", err)
	}
	if fetcher.appendCalls != 1 {
		t.Fatalf("applied retry APPEND calls=%d, want 1", fetcher.appendCalls)
	}
	_, state, uid, uidValidity = transferState(t, fixture)
	if state != "consumed" || uid != 1 || uidValidity != 701 {
		t.Fatalf("completed retry state=%q uid=%d uidvalidity=%d, want consumed/1/701", state, uid, uidValidity)
	}
}

func TestCopyMessageUsesFreshUIDValidityAndConsumesInboxTransfer(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	if err := fixture.store.UpdateMailboxRemoteStatus(context.Background(), fixture.userID,
		fixture.source.ID, 1, 0, 43, 111); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE messages SET uid_validity = 111 WHERE user_id = ? AND id = ?`, fixture.userID, fixture.message.ID); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <fresh-copy@example.test>\r\nFrom: sender@example.test\r\nTo: move-hook@example.test\r\nSubject: fresh copy\r\n\r\nbody\r\n")
	fetcher := &copyJournalTestFetcher{
		moveTestFetcher:  fixture.fetcher,
		raw:              raw,
		fetchUIDValidity: 111,
		appendResult: FetchedMessage{
			Mailbox: fixture.source.Name, UID: 73, UIDValidity: 222,
			AppendUIDAuthoritative: true,
			InternalDate:           fixture.message.InternalDate, Size: int64(len(raw)),
			Flags: []string{"\\Seen", "\\Flagged"}, Raw: raw,
		},
	}
	fixture.service.Fetcher = fetcher

	if err := fixture.service.CopyMessage(context.Background(), fixture.userID,
		fixture.message.ID, fixture.source.ID); err != nil {
		var transferUID, transferValidity, messageUID, messageValidity uint32
		_ = fixture.store.DB().QueryRow(`SELECT destination_uid, destination_uid_validity
			FROM message_transfers WHERE user_id = ? ORDER BY id DESC LIMIT 1`, fixture.userID).
			Scan(&transferUID, &transferValidity)
		_ = fixture.store.DB().QueryRow(`SELECT uid, uid_validity FROM messages
			WHERE user_id = ? AND mailbox_id = ? AND uid = 73`, fixture.userID, fixture.source.ID).
			Scan(&messageUID, &messageValidity)
		t.Fatalf("%v; transfer uid=%d validity=%d message uid=%d validity=%d", err,
			transferUID, transferValidity, messageUID, messageValidity)
	}
	kind, state, uid, uidValidity := transferState(t, fixture)
	if kind != "copy" || state != "consumed" || uid != 73 || uidValidity != 222 {
		t.Fatalf("transfer kind=%q state=%q uid=%d uidvalidity=%d", kind, state, uid, uidValidity)
	}
	var storedUIDValidity int64
	if err := fixture.store.DB().QueryRow(`SELECT uid_validity FROM messages
		WHERE user_id = ? AND mailbox_id = ? AND uid = ?`, fixture.userID, fixture.source.ID, 73).
		Scan(&storedUIDValidity); err != nil {
		t.Fatal(err)
	}
	if storedUIDValidity != 222 {
		t.Fatalf("stored uid_validity=%d, want 222", storedUIDValidity)
	}
	arrivalUIDFloor, err := fixture.store.MailboxGenerationRebuildArrivalUIDFloor(context.Background(),
		fixture.userID, fixture.account.ID, fixture.source.ID, 222)
	if err != nil || arrivalUIDFloor != 74 {
		t.Fatalf("inbox copy rebuild arrival floor=%d err=%v, want 74/nil", arrivalUIDFloor, err)
	}
	var classification string
	if err := fixture.store.DB().QueryRow(`SELECT classification FROM pending_inbox_arrivals
		WHERE user_id = ? AND mailbox_id = ? AND message_id IN (
			SELECT id FROM messages WHERE user_id = ? AND mailbox_id = ? AND uid = ?)`,
		fixture.userID, fixture.source.ID, fixture.userID, fixture.source.ID, 73).Scan(&classification); err != nil {
		t.Fatal(err)
	}
	if classification != string(store.ArrivalLocalCopy) {
		t.Fatalf("arrival classification=%q, want %q", classification, store.ArrivalLocalCopy)
	}
}

func TestCopyMessageNeverAdvancesLastUIDOutOfBand(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authoritative bool
	}{
		{name: "content-correlated fallback", authoritative: false},
		{name: "APPENDUID receipt", authoritative: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMoveTestFixture(t)
			fixture.service.Blobs = blob.New(t.TempDir())
			if err := fixture.store.UpdateMailboxLastUID(context.Background(), fixture.userID, fixture.destination.ID, 50); err != nil {
				t.Fatal(err)
			}
			raw := []byte("Message-ID: <copy-checkpoint@example.test>\r\nFrom: sender@example.test\r\nTo: move-hook@example.test\r\nSubject: copy checkpoint\r\n\r\nbody\r\n")
			fixture.service.Fetcher = &copyJournalTestFetcher{
				moveTestFetcher: fixture.fetcher,
				raw:             raw,
				appendResult: FetchedMessage{
					Mailbox: fixture.destination.Name, UID: 73, UIDValidity: uint32(fixture.destination.UIDValidity),
					AppendUIDAuthoritative: tc.authoritative,
					InternalDate:           fixture.message.InternalDate, Size: int64(len(raw)),
					Flags: []string{"\\Seen", "\\Flagged"}, Raw: raw,
				},
			}

			if err := fixture.service.CopyMessage(context.Background(), fixture.userID,
				fixture.message.ID, fixture.destination.ID); err != nil {
				t.Fatal(err)
			}
			mailbox, err := fixture.store.GetMailboxForUser(context.Background(), fixture.userID, fixture.destination.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mailbox.LastUID != 50 {
				t.Fatalf("last_uid = %d, want unchanged checkpoint 50", mailbox.LastUID)
			}
		})
	}
}

func TestCopyMessageResetsDestinationBeforeReusedUID(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	const (
		staleUIDValidity = 701
		freshUIDValidity = 902
		reusedUID        = 73
	)
	if err := fixture.store.UpdateMailboxRemoteStatus(context.Background(), fixture.userID,
		fixture.destination.ID, 1, 0, 81, staleUIDValidity); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.UpdateMailboxLastUID(context.Background(), fixture.userID, fixture.destination.ID, 80); err != nil {
		t.Fatal(err)
	}
	staleBlob, err := fixture.store.CreateBlob(context.Background(), store.BlobRecord{
		UserID: fixture.userID, Kind: "message-remote", Path: "stale-destination.eml", SHA256: "stale", Size: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := fixture.store.CreateMessage(context.Background(), store.CreateMessage{
		UserID: fixture.userID, AccountID: fixture.account.ID, MailboxID: fixture.destination.ID,
		BlobID: staleBlob.ID, MessageIDHeader: "<stale-copy@example.test>", Subject: "stale epoch",
		Date: fixture.message.Date, InternalDate: fixture.message.InternalDate, UID: reusedUID,
		UIDValidity: staleUIDValidity, Size: staleBlob.Size, IsRead: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <fresh-copy@example.test>\r\nFrom: sender@example.test\r\nTo: move-hook@example.test\r\nSubject: fresh epoch\r\n\r\nbody\r\n")
	fixture.service.Fetcher = &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             raw,
		appendResult: FetchedMessage{
			Mailbox: fixture.destination.Name, UID: reusedUID, UIDValidity: freshUIDValidity,
			InternalDate: fixture.message.InternalDate, Size: int64(len(raw)),
			Flags: []string{"\\Seen", "\\Flagged"}, Raw: raw,
		},
	}

	if err := fixture.service.CopyMessage(context.Background(), fixture.userID,
		fixture.message.ID, fixture.destination.ID); err != nil {
		t.Fatal(err)
	}
	destination, err := fixture.store.GetMailboxForUser(context.Background(), fixture.userID, fixture.destination.ID)
	if err != nil {
		t.Fatal(err)
	}
	if destination.UIDValidity != freshUIDValidity || destination.LastUID != 0 {
		t.Fatalf("destination uidvalidity=%d last_uid=%d, want %d/0", destination.UIDValidity, destination.LastUID, freshUIDValidity)
	}
	arrivalUIDFloor, err := fixture.store.MailboxGenerationRebuildArrivalUIDFloor(context.Background(),
		fixture.userID, fixture.account.ID, fixture.destination.ID, freshUIDValidity)
	if err != nil || arrivalUIDFloor != reusedUID+1 {
		t.Fatalf("destination rebuild arrival floor=%d err=%v, want %d/nil", arrivalUIDFloor, err, reusedUID+1)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, stale.ID); !store.IsNotFound(err) {
		t.Fatalf("stale destination row survived generation reset: %v", err)
	}
	copied, err := fixture.store.GetMessageByUID(context.Background(), fixture.userID,
		fixture.account.ID, fixture.destination.ID, reusedUID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == stale.ID || copied.Subject != "fresh epoch" {
		t.Fatalf("reused UID resolved to stale row: %+v", copied)
	}
	copiedUIDValidity, err := fixture.store.GetMessageUIDValidityForUser(context.Background(), fixture.userID, copied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copiedUIDValidity != freshUIDValidity {
		t.Fatalf("copied uid_validity=%d, want %d", copiedUIDValidity, freshUIDValidity)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); err != nil {
		t.Fatalf("source message removed during destination reset: %v", err)
	}
	var tombstones int
	if err := fixture.store.DB().QueryRow(`SELECT COUNT(*) FROM expunged_message_fingerprints
		WHERE user_id = ? AND source_mailbox_id = ?`, fixture.userID, fixture.destination.ID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("destination generation reset created %d expunge tombstones", tombstones)
	}
}

func TestCopyMessageSchedulesUnexpectedPendingInboxArrival(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	sourceRaw := []byte("Message-ID: <pending-copy@example.test>\r\nFrom: sender@example.test\r\nTo: move-hook@example.test\r\nSubject: pending copy\r\n\r\nsource body\r\n")
	serverRaw := []byte("Message-ID: <pending-copy@example.test>\r\nFrom: sender@example.test\r\nTo: move-hook@example.test\r\nSubject: pending copy\r\n\r\nserver-rewritten body\r\n")
	fixture.service.Fetcher = &copyJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		raw:             sourceRaw,
		appendResult: FetchedMessage{
			Mailbox: fixture.source.Name, UID: 73, UIDValidity: moveTestSourceUIDValidity,
			InternalDate: fixture.message.InternalDate, Size: int64(len(serverRaw)),
			Flags: []string{"\\Seen", "\\Flagged"}, Raw: serverRaw,
		},
	}
	var scheduledUserID, scheduledAccountID int64
	var scheduledAt time.Time
	fixture.service.ScheduleInboxArrival = func(userID, accountID int64, due time.Time) {
		scheduledUserID = userID
		scheduledAccountID = accountID
		scheduledAt = due
	}

	if err := fixture.service.CopyMessage(context.Background(), fixture.userID,
		fixture.message.ID, fixture.source.ID); err != nil {
		t.Fatal(err)
	}
	if scheduledUserID != fixture.userID || scheduledAccountID != fixture.account.ID || scheduledAt.IsZero() {
		t.Fatalf("scheduled arrival user=%d account=%d due=%s", scheduledUserID, scheduledAccountID, scheduledAt)
	}
	var classification string
	if err := fixture.store.DB().QueryRow(`SELECT classification FROM pending_inbox_arrivals
		WHERE user_id = ? AND account_id = ? AND mailbox_id = ? AND message_id IN (
			SELECT id FROM messages WHERE user_id = ? AND mailbox_id = ? AND uid = ?)`,
		fixture.userID, fixture.account.ID, fixture.source.ID,
		fixture.userID, fixture.source.ID, 73).Scan(&classification); err != nil {
		t.Fatal(err)
	}
	if classification != string(store.ArrivalPending) {
		t.Fatalf("arrival classification=%q, want %q", classification, store.ArrivalPending)
	}
}

func TestStoreFetchedMessagePersistsArrivalFingerprints(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fixture.service.Blobs = blob.New(t.TempDir())
	raw := []byte("Message-ID: <stored-fingerprint@example.test>\r\nFrom: sender@example.test\r\nTo: receiver@example.test\r\nSubject: Stored fingerprint\r\nDate: Tue, 14 Jul 2026 12:00:00 +0000\r\n\r\nbody\r\n")
	internalDate := time.Date(2026, 7, 14, 12, 1, 0, 0, time.UTC)
	item := FetchedMessage{Mailbox: fixture.destination.Name, UID: 74, InternalDate: internalDate, Raw: raw}

	msg, _, _, err := fixture.service.storeFetchedMessage(context.Background(), fixture.userID, fixture.account, fixture.destination, item, true)
	if err != nil {
		t.Fatal(err)
	}
	want := store.MessageArrivalFingerprint(raw, "<stored-fingerprint@example.test>", internalDate, 0)
	var canonical, messageIDHash string
	var size int64
	if err := fixture.store.DB().QueryRow(`SELECT canonical_sha256, message_id_hash, size FROM messages
		WHERE user_id = ? AND id = ?`, fixture.userID, msg.ID).Scan(&canonical, &messageIDHash, &size); err != nil {
		t.Fatal(err)
	}
	if canonical != want.CanonicalSHA256 || messageIDHash != want.MessageIDHash || size != int64(len(raw)) {
		t.Fatalf("stored fingerprint canonical=%q message_id=%q size=%d, want %q %q %d",
			canonical, messageIDHash, size, want.CanonicalSHA256, want.MessageIDHash, len(raw))
	}
}

func TestReconcileMailboxRecordsFingerprintBeforeDeletingSource(t *testing.T) {
	fixture := newMoveTestFixture(t)
	const sourceUIDValidity = 777
	if err := fixture.store.UpdateMailboxRemoteStatus(context.Background(), fixture.userID,
		fixture.source.ID, 1, 0, fixture.message.UID+1, sourceUIDValidity); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`UPDATE messages SET uid_validity = ? WHERE user_id = ? AND id = ?`,
		sourceUIDValidity, fixture.userID, fixture.message.ID); err != nil {
		t.Fatal(err)
	}
	raw := []byte("Message-ID: <expunged@example.test>\r\nSubject: expunged\r\n\r\nbody\r\n")
	fingerprint := store.MessageArrivalFingerprint(raw, fixture.message.MessageIDHeader, fixture.message.InternalDate, fixture.message.Size)
	if err := fixture.store.SetMessageArrivalFingerprint(context.Background(), fixture.userID, fixture.message.ID, fingerprint); err != nil {
		t.Fatal(err)
	}
	fetcher := &reconcileJournalTestFetcher{
		moveTestFetcher: fixture.fetcher,
		uidValidity:     sourceUIDValidity,
		uidNext:         fixture.message.UID + 1,
	}
	fixture.service.Fetcher = fetcher

	if err := fixture.service.reconcileMailboxUIDs(context.Background(), fixture.userID, fixture.account, fixture.source); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); !store.IsNotFound(err) {
		t.Fatalf("stale source message still present: %v", err)
	}
	var userID, mailboxID int64
	var sourceUID uint32
	var canonical string
	if err := fixture.store.DB().QueryRow(`SELECT user_id, source_mailbox_id, source_uid, canonical_sha256
		FROM expunged_message_fingerprints WHERE user_id = ?`, fixture.userID).
		Scan(&userID, &mailboxID, &sourceUID, &canonical); err != nil {
		t.Fatal(err)
	}
	if userID != fixture.userID || mailboxID != fixture.source.ID || sourceUID != fixture.message.UID || canonical != fingerprint.CanonicalSHA256 {
		t.Fatalf("expunge fingerprint user=%d mailbox=%d uid=%d canonical=%q", userID, mailboxID, sourceUID, canonical)
	}
	if fetcher.snapshotCalls != 1 || fetcher.legacyCalls != 0 {
		t.Fatalf("reconcile snapshot calls=%d legacy UID calls=%d, want 1/0", fetcher.snapshotCalls, fetcher.legacyCalls)
	}
}
