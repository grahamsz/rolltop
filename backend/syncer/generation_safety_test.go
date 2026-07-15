package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rolltop/backend/store"
)

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
