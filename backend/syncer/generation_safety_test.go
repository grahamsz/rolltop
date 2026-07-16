package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

type generationRecoveryBatchFetcher struct {
	*statusSelectRaceFetcher
	batches [][]uint32
}

func (f *generationRecoveryBatchFetcher) FetchUIDsWithUIDValidity(
	ctx context.Context,
	_ store.MailAccount,
	mailbox string,
	uids []uint32,
	expectedUIDValidity uint32,
	handle func(FetchedMessage) error,
) error {
	f.batches = append(f.batches, append([]uint32(nil), uids...))
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := handle(FetchedMessage{Mailbox: mailbox, UID: uid, UIDValidity: expectedUIDValidity}); err != nil {
			return err
		}
	}
	return nil
}

func TestLargeGenerationSnapshotRequiresThreeRecoveryTurns(t *testing.T) {
	fetcher := &generationRecoveryBatchFetcher{statusSelectRaceFetcher: &statusSelectRaceFetcher{
		moveTestFetcher:  fixturelessMoveFetcher(),
		selectedValidity: 41,
	}}
	service := &Service{Fetcher: fetcher}
	uids := make([]uint32, 1001)
	for i := range uids {
		uids[i] = uint32(i + 1)
	}
	var handled []uint32
	var refreshes []bool
	snapshot := MailboxUIDSnapshot{UIDs: uids, UIDValidity: 41, UIDNext: 1002}
	afterUID := uint32(0)
	wantBatchSizes := []int{500, 500, 1}
	for turn, wantBatchSize := range wantBatchSizes {
		handledBefore := len(handled)
		complete, err := service.fetchMailboxGenerationSnapshotBatch(context.Background(), store.MailAccount{},
			store.Mailbox{Name: "INBOX"}, afterUID, 41, snapshot,
			func(item FetchedMessage) error {
				handled = append(handled, item.UID)
				return nil
			}, func(final bool) error {
				refreshes = append(refreshes, final)
				return nil
			})
		if err != nil {
			t.Fatalf("recovery turn %d: %v", turn+1, err)
		}
		wantComplete := turn == len(wantBatchSizes)-1
		if complete != wantComplete {
			t.Fatalf("recovery turn %d complete=%t, want %t", turn+1, complete, wantComplete)
		}
		if got := len(handled) - handledBefore; got != wantBatchSize {
			t.Fatalf("recovery turn %d handled=%d, want %d", turn+1, got, wantBatchSize)
		}
		afterUID = handled[len(handled)-1]
	}
	if len(fetcher.batches) != 3 || len(fetcher.batches[0]) != 500 ||
		len(fetcher.batches[1]) != 500 || len(fetcher.batches[2]) != 1 {
		t.Fatalf("recovery batches=%v, want sizes 500/500/1", fetcher.batches)
	}
	if fetcher.batches[0][0] != 1 || fetcher.batches[0][499] != 500 ||
		fetcher.batches[1][0] != 501 || fetcher.batches[1][499] != 1000 ||
		fetcher.batches[2][0] != 1001 {
		t.Fatalf("recovery batch boundaries=%v, want 1..500, 501..1000, 1001", fetcher.batches)
	}
	if len(handled) != len(uids) || handled[0] != 1 || handled[len(handled)-1] != 1001 {
		t.Fatalf("handled UIDs count=%d first=%d last=%d", len(handled), handled[0], handled[len(handled)-1])
	}
	wantRefreshes := []bool{false, false, true}
	if len(refreshes) != len(wantRefreshes) {
		t.Fatalf("refreshes=%v, want %v", refreshes, wantRefreshes)
	}
	for i := range wantRefreshes {
		if refreshes[i] != wantRefreshes[i] {
			t.Fatalf("refreshes=%v, want %v", refreshes, wantRefreshes)
		}
	}
}

type statusSelectRaceFetcher struct {
	*moveTestFetcher
	statusValidity   uint32
	selectedValidity uint32
	legacyFetchCalls int
	boundFetchCalls  int
	sparseFetchCalls int
}

func (f *statusSelectRaceFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	return MailboxStatus{Messages: 1, UIDNext: 43, UIDValidity: f.statusValidity}, nil
}

func (f *statusSelectRaceFetcher) FetchMailbox(context.Context, store.MailAccount, string, uint32, func(FetchedMessage) error) error {
	f.legacyFetchCalls++
	return nil
}

func (f *statusSelectRaceFetcher) FetchMailboxWithUIDValidity(_ context.Context, _ store.MailAccount, mailbox string, _ uint32, expectedUIDValidity uint32, _ func(FetchedMessage) error) error {
	f.boundFetchCalls++
	if expectedUIDValidity != f.selectedValidity {
		return errors.New("selected mailbox UIDVALIDITY changed")
	}
	return nil
}

func (f *statusSelectRaceFetcher) FetchUIDsWithUIDValidity(_ context.Context, _ store.MailAccount, _ string, _ []uint32, _ uint32, _ func(FetchedMessage) error) error {
	f.sparseFetchCalls++
	return nil
}

func TestMailboxFetchAbortsBeforeHandlerWhenSELECTGenerationDiffersFromSTATUS(t *testing.T) {
	fetcher := &statusSelectRaceFetcher{
		moveTestFetcher: fixturelessMoveFetcher(),
		statusValidity:  1, selectedValidity: 2,
	}
	service := &Service{Fetcher: fetcher}
	status, err := fetcher.MailboxStatus(context.Background(), store.MailAccount{}, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	handled := 0
	err = service.fetchMailboxForGeneration(context.Background(), store.MailAccount{}, "INBOX", 0, status.UIDValidity, func(FetchedMessage) error {
		handled++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "UIDVALIDITY") {
		t.Fatalf("fetch error = %v, want SELECT generation rejection", err)
	}
	if handled != 0 || fetcher.boundFetchCalls != 1 || fetcher.legacyFetchCalls != 0 {
		t.Fatalf("handled=%d bound calls=%d legacy calls=%d, want 0/1/0", handled, fetcher.boundFetchCalls, fetcher.legacyFetchCalls)
	}
}

type unboundMailboxFetcher struct {
	*moveTestFetcher
	legacyCalls int
}

func (f *unboundMailboxFetcher) FetchMailbox(context.Context, store.MailAccount, string, uint32, func(FetchedMessage) error) error {
	f.legacyCalls++
	return nil
}

func TestMailboxFetchRejectsUnboundFetcherWithoutCallingLegacyPath(t *testing.T) {
	fetcher := &unboundMailboxFetcher{moveTestFetcher: fixturelessMoveFetcher()}
	service := &Service{Fetcher: fetcher}
	handled := 0
	err := service.fetchMailboxForGeneration(context.Background(), store.MailAccount{}, "INBOX", 0, 41, func(FetchedMessage) error {
		handled++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "cannot prove mailbox generation") {
		t.Fatalf("unbound fetch error=%v", err)
	}
	if fetcher.legacyCalls != 0 || handled != 0 {
		t.Fatalf("legacy calls=%d handled=%d, want 0/0", fetcher.legacyCalls, handled)
	}
}

type repairSnapshotRaceFetcher struct {
	*statusSelectRaceFetcher
	snapshot    MailboxUIDSnapshot
	snapshotErr error
	legacyUIDs  int
}

func (f *repairSnapshotRaceFetcher) SnapshotMailboxUIDs(context.Context, store.MailAccount, string) (MailboxUIDSnapshot, error) {
	return f.snapshot, f.snapshotErr
}

func (f *repairSnapshotRaceFetcher) UIDs(context.Context, store.MailAccount, string) ([]uint32, error) {
	f.legacyUIDs++
	return nil, nil
}

func TestSparseRepairRejectsSnapshotFromDifferentGeneration(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &repairSnapshotRaceFetcher{
		statusSelectRaceFetcher: &statusSelectRaceFetcher{
			moveTestFetcher:  fixture.fetcher,
			statusValidity:   moveTestSourceUIDValidity,
			selectedValidity: moveTestSourceUIDValidity,
		},
		snapshot: MailboxUIDSnapshot{
			UIDs: []uint32{42, 43}, UIDValidity: moveTestSourceUIDValidity + 1, UIDNext: 44,
		},
	}
	fixture.service.Fetcher = fetcher
	plan := MailboxPlan{
		Name: fixture.source.Name, LastUID: fixture.source.LastUID,
		Status: MailboxStatus{Messages: 2, UIDNext: 44, UIDValidity: moveTestSourceUIDValidity},
	}
	_, repaired, err := fixture.service.repairRequestedIncompleteMailbox(context.Background(), fixture.userID,
		fixture.account, fixture.source, plan, true, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("repair error = %v, want generation rejection", err)
	}
	if repaired || fetcher.sparseFetchCalls != 0 || fetcher.legacyUIDs != 0 {
		t.Fatalf("repaired=%t sparse calls=%d legacy UID calls=%d, want false/0/0", repaired, fetcher.sparseFetchCalls, fetcher.legacyUIDs)
	}
	if _, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID); err != nil {
		t.Fatalf("snapshot mismatch changed local message: %v", err)
	}
}

type generationRefusingFlagFetcher struct {
	*moveTestFetcher
	seenWriteCalls   int
	seenReadCalls    int
	flaggedReadCalls int
}

func (f *generationRefusingFlagFetcher) SetSeenWithUIDValidity(context.Context, store.MailAccount, string, uint32, bool, uint32) (bool, error) {
	f.seenWriteCalls++
	return false, nil
}

func (f *generationRefusingFlagFetcher) SeenUIDsWithUIDValidity(context.Context, store.MailAccount, string, uint32) ([]uint32, bool, error) {
	f.seenReadCalls++
	return []uint32{}, false, nil
}

func (f *generationRefusingFlagFetcher) FlaggedUIDsWithUIDValidity(context.Context, store.MailAccount, string, uint32) ([]uint32, bool, error) {
	f.flaggedReadCalls++
	return []uint32{}, false, nil
}

func TestPendingAndPulledFlagsStayLocalAcrossGenerationChange(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &generationRefusingFlagFetcher{moveTestFetcher: fixture.fetcher}
	fixture.service.Fetcher = fetcher
	if err := fixture.store.MarkMessageReadForUser(context.Background(), fixture.userID, fixture.message.ID, false, true); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SyncReadStateForMessage(context.Background(), fixture.userID, fixture.message.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.syncMailboxReadFlags(context.Background(), fixture.userID, fixture.account, fixture.source); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.syncMailboxStarFlags(context.Background(), fixture.userID, fixture.account, fixture.source); err != nil {
		t.Fatal(err)
	}
	msg, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.IsRead || !msg.ReadSyncPending || !msg.IsStarred {
		t.Fatalf("local flags changed across generation mismatch: read=%t pending=%t starred=%t", msg.IsRead, msg.ReadSyncPending, msg.IsStarred)
	}
	if fetcher.seenWriteCalls != 1 || fetcher.seenReadCalls != 1 || fetcher.flaggedReadCalls != 1 {
		t.Fatalf("generation-bound calls write/read/star=%d/%d/%d, want 1/1/1", fetcher.seenWriteCalls, fetcher.seenReadCalls, fetcher.flaggedReadCalls)
	}
}

func TestGenerationRecoveryDefersTenantPendingFlagUploads(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &generationRefusingFlagFetcher{moveTestFetcher: fixture.fetcher}
	fixture.service.Fetcher = fetcher
	if err := fixture.store.MarkMessageReadForUser(context.Background(), fixture.userID, fixture.message.ID, false, true); err != nil {
		t.Fatal(err)
	}

	// A regular sync still drains pending local flag changes before fetching.
	if _, err := fixture.service.SyncUserAccountMailboxes(context.Background(), fixture.userID,
		fixture.account.ID, []string{fixture.source.Name}); err == nil {
		t.Fatal("regular sync unexpectedly succeeded with the refusing test fetcher")
	}
	if fetcher.seenWriteCalls != 1 {
		t.Fatalf("regular sync Seen writes=%d, want 1", fetcher.seenWriteCalls)
	}

	// An unrelated account-qualified poll admitted during recovery uses the
	// normal fetch path but must not drain tenant-wide pending flags.
	if _, err := fixture.service.syncUserAccountMailboxes(context.Background(), fixture.userID,
		fixture.account.ID, []string{fixture.source.Name}, syncAccountOptions{deferPendingFlags: true}); err == nil {
		t.Fatal("deferred-flag sync unexpectedly succeeded with the refusing test fetcher")
	}
	if fetcher.seenWriteCalls != 1 {
		t.Fatalf("deferred-flag sync performed pending Seen writes: calls=%d, want 1", fetcher.seenWriteCalls)
	}

	// A generation-recovery turn must start fetching the requested mailbox
	// without first performing up to 500 unrelated writes for the tenant.
	if _, err := fixture.service.RecoverUserAccountMailboxGeneration(context.Background(), fixture.userID,
		fixture.account.ID, fixture.source.Name); err == nil {
		t.Fatal("generation recovery unexpectedly succeeded with the refusing test fetcher")
	}
	if fetcher.seenWriteCalls != 1 {
		t.Fatalf("generation recovery performed pending Seen writes: calls=%d, want 1", fetcher.seenWriteCalls)
	}
	message, err := fixture.store.GetMessageForUser(context.Background(), fixture.userID, fixture.message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !message.ReadSyncPending {
		t.Fatal("generation recovery cleared the deferred local Seen change")
	}
}

func TestRunnerPollDuringGenerationRecoveryDefersTenantPendingFlagUploads(t *testing.T) {
	fixture := newMoveTestFixture(t)
	fetcher := &generationRefusingFlagFetcher{moveTestFetcher: fixture.fetcher}
	fixture.service.Fetcher = fetcher
	if err := fixture.store.MarkMessageReadForUser(context.Background(), fixture.userID, fixture.message.ID, false, true); err != nil {
		t.Fatal(err)
	}

	runner := NewRunnerWithContext(context.Background(), fixture.service)
	runner.mu.Lock()
	runner.generationRecoveryUsers[fixture.userID] = true
	runner.generationRecoveryRuns[fixture.userID] = true
	runner.generationRecoveryKnown[fixture.userID] = true
	runner.generationRecoveryAccounts[fixture.userID] = map[int64]bool{fixture.account.ID: true}
	runner.generationRecoveryTargets[fixture.userID] = map[string]bool{
		accountMailboxKey(fixture.userID, fixture.account.ID, "Gmail Forward"): true,
	}
	runner.mu.Unlock()
	keys, ok := runner.reserveAccountMailboxes(fixture.userID, fixture.account.ID, []string{fixture.source.Name})
	if !ok {
		t.Fatal("unrelated Inbox poll was blocked by generation recovery")
	}
	runner.runReservedAccountMailboxes(fixture.userID, fixture.account.ID, []string{fixture.source.Name}, keys)
	if fetcher.seenWriteCalls != 0 {
		t.Fatalf("runner poll drained tenant Seen writes during recovery: calls=%d", fetcher.seenWriteCalls)
	}
}

type serializedGenerationDiscoveryFetcher struct {
	*moveTestFetcher
	snapshotCalls int
	sparseCalls   int
	boundCalls    int
}

func (f *serializedGenerationDiscoveryFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	return MailboxStatus{Messages: 1, Unseen: 1, UIDNext: 2, UIDValidity: 2}, nil
}

func (f *serializedGenerationDiscoveryFetcher) SnapshotMailboxUIDs(context.Context, store.MailAccount, string) (MailboxUIDSnapshot, error) {
	f.snapshotCalls++
	return MailboxUIDSnapshot{UIDs: []uint32{1}, UIDValidity: 2, UIDNext: 2}, nil
}

func (f *serializedGenerationDiscoveryFetcher) FetchMailboxWithUIDValidity(context.Context, store.MailAccount,
	string, uint32, uint32, func(FetchedMessage) error,
) error {
	f.boundCalls++
	return nil
}

func (f *serializedGenerationDiscoveryFetcher) FetchUIDsWithUIDValidity(_ context.Context, _ store.MailAccount,
	mailbox string, uids []uint32, expectedUIDValidity uint32, handle func(FetchedMessage) error,
) error {
	f.sparseCalls++
	for _, uid := range uids {
		if err := handle(FetchedMessage{
			Mailbox: mailbox, UID: uid, UIDValidity: expectedUIDValidity,
			InternalDate: time.Date(2026, 7, 16, 8, 30, 0, 0, time.UTC),
			Raw: []byte("From: sender@example.test\r\nTo: owner@example.test\r\n" +
				"Subject: recovered\r\nMessage-ID: <recovered@example.test>\r\n\r\nbody\r\n"),
		}); err != nil {
			return err
		}
	}
	return nil
}

func TestRunnerServiceYieldsDiscoveredGenerationToSerializedRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "serialized-generation@example.test", "Serialized Generation", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "serialized-generation")
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, mailbox.ID, 1, 1, 2, 1); err != nil {
		t.Fatal(err)
	}

	fetcher := &serializedGenerationDiscoveryFetcher{moveTestFetcher: &moveTestFetcher{}}
	service := &Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher}
	runner := NewRunnerWithContext(ctx, service)
	if !service.DeferMailboxGenerationRebuilds {
		t.Fatal("Runner did not enable serialized mailbox generation discovery")
	}
	// Keep the test deterministic while retaining the Runner-installed policy.
	// The production callback wakes the same recovery entrypoint invoked below.
	signals := 0
	service.MailboxGenerationRecoveryStarted = func(signalUserID int64) {
		if signalUserID != user.ID {
			t.Fatalf("recovery signal user_id=%d, want %d", signalUserID, user.ID)
		}
		signals++
	}
	_ = runner

	if _, err := service.SyncUserAccountMailboxes(ctx, user.ID, account.ID, []string{"INBOX"}); err != nil {
		t.Fatal(err)
	}
	if fetcher.snapshotCalls != 0 || fetcher.sparseCalls != 0 || fetcher.boundCalls != 0 {
		t.Fatalf("ordinary sync started generation fetch snapshot/sparse/bound=%d/%d/%d, want 0/0/0",
			fetcher.snapshotCalls, fetcher.sparseCalls, fetcher.boundCalls)
	}
	if signals != 1 {
		t.Fatalf("generation recovery signals=%d, want 1", signals)
	}
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, account.ID, mailbox.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("ordinary sync did not retain the durable generation marker")
	}
	mailbox, err = db.GetMailboxForUser(ctx, user.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mailbox.UIDValidity != 2 || mailbox.LastUID != 0 || mailbox.RemoteMessageCount != 1 ||
		mailbox.RemoteUnreadCount != 1 || mailbox.RemoteUIDNext != 2 {
		t.Fatalf("yielded mailbox status=%+v, want generation 2, checkpoint 0, remote 1/1/2", mailbox)
	}

	if _, err := service.RecoverUserAccountMailboxGeneration(ctx, user.ID, account.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if fetcher.snapshotCalls == 0 || fetcher.sparseCalls == 0 {
		t.Fatalf("designated recovery did not fetch generation snapshot/sparse=%d/%d",
			fetcher.snapshotCalls, fetcher.sparseCalls)
	}
	pending, err = db.MailboxGenerationRebuildExists(ctx, user.ID, account.ID, mailbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("designated recovery did not finalize the generation marker")
	}
}

type recordingCopyFlagFetcher struct {
	*moveTestFetcher
	flaggedValidity uint32
}

func (f *recordingCopyFlagFetcher) SetFlaggedWithUIDValidity(_ context.Context, _ store.MailAccount, _ string, _ uint32, _ bool, expectedUIDValidity uint32) (bool, error) {
	f.flaggedValidity = expectedUIDValidity
	return true, nil
}

func TestCopiedMessageFlagWriteUsesAppendGeneration(t *testing.T) {
	fetcher := &recordingCopyFlagFetcher{moveTestFetcher: fixturelessMoveFetcher()}
	service := &Service{Fetcher: fetcher}
	source := store.MessageRecord{IsRead: true, IsStarred: true}
	fetched := FetchedMessage{UID: 73, UIDValidity: 902}
	if err := service.applyCopiedMessageFlags(context.Background(), store.MailAccount{}, store.Mailbox{Name: "Archive"}, fetched.UID, source, &fetched); err != nil {
		t.Fatal(err)
	}
	if fetcher.flaggedValidity != fetched.UIDValidity || !hasFlagged(fetched.Flags) {
		t.Fatalf("flag write UIDVALIDITY=%d flags=%v, want %d and Flagged", fetcher.flaggedValidity, fetched.Flags, fetched.UIDValidity)
	}
}

func fixturelessMoveFetcher() *moveTestFetcher {
	return &moveTestFetcher{}
}

func TestArrivalUIDFloorAfterConfirmedUIDIsExclusiveAndOverflowSafe(t *testing.T) {
	floor, err := ArrivalUIDFloorAfterConfirmedUID(73)
	if err != nil || floor != 74 {
		t.Fatalf("arrival floor=%d err=%v, want 74/nil", floor, err)
	}
	for _, uid := range []uint32{0, ^uint32(0)} {
		if floor, err := ArrivalUIDFloorAfterConfirmedUID(uid); err == nil || floor != 0 {
			t.Fatalf("invalid UID %d floor=%d err=%v, want 0/error", uid, floor, err)
		}
	}
}
