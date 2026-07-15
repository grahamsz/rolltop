package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/store"
)

type boundedRecoveryFairnessFetcher struct {
	*moveTestFetcher
	recoveryAccountID int64
	healthyAccountID  int64
	messages          []FetchedMessage
	historyStarted    chan struct{}
	releaseHistory    chan struct{}
	healthyStarted    chan struct{}
	releaseHealthy    chan struct{}
	historyOnce       sync.Once
	healthyOnce       sync.Once
}

func (f *boundedRecoveryFairnessFetcher) ListMailboxes(context.Context, store.MailAccount) ([]MailboxInfo, error) {
	return []MailboxInfo{{Name: "INBOX"}}, nil
}

func (f *boundedRecoveryFairnessFetcher) MailboxStatus(_ context.Context, account store.MailAccount, _ string) (MailboxStatus, error) {
	if account.ID == f.recoveryAccountID {
		return MailboxStatus{Messages: uint32(len(f.messages)), UIDNext: uint32(len(f.messages) + 1), UIDValidity: 2}, nil
	}
	return MailboxStatus{UIDNext: 1, UIDValidity: 1}, nil
}

func (f *boundedRecoveryFairnessFetcher) SnapshotMailboxUIDs(ctx context.Context, account store.MailAccount, mailbox string) (MailboxUIDSnapshot, error) {
	status, err := f.MailboxStatus(ctx, account, mailbox)
	if err != nil {
		return MailboxUIDSnapshot{}, err
	}
	snapshot := MailboxUIDSnapshot{UIDValidity: status.UIDValidity, UIDNext: status.UIDNext}
	if account.ID == f.recoveryAccountID {
		snapshot.UIDs = make([]uint32, len(f.messages))
		for i := range f.messages {
			snapshot.UIDs[i] = f.messages[i].UID
		}
	}
	return snapshot, nil
}

func (f *boundedRecoveryFairnessFetcher) UIDs(ctx context.Context, account store.MailAccount, mailbox string) ([]uint32, error) {
	snapshot, err := f.SnapshotMailboxUIDs(ctx, account, mailbox)
	return snapshot.UIDs, err
}

func (f *boundedRecoveryFairnessFetcher) FetchUIDsWithUIDValidity(
	ctx context.Context,
	account store.MailAccount,
	mailbox string,
	uids []uint32,
	expectedUIDValidity uint32,
	handle func(FetchedMessage) error,
) error {
	if account.ID != f.recoveryAccountID || expectedUIDValidity != 2 {
		return fmt.Errorf("unexpected generation fetch account=%d validity=%d", account.ID, expectedUIDValidity)
	}
	if len(uids) == mailboxGenerationRecoveryBatchSize && uids[0] == 1 {
		f.historyOnce.Do(func() { close(f.historyStarted) })
		select {
		case <-f.releaseHistory:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, uid := range uids {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := f.messages[uid-1]
		item.Mailbox = mailbox
		item.UIDValidity = expectedUIDValidity
		if err := handle(item); err != nil {
			return err
		}
	}
	return nil
}

func (f *boundedRecoveryFairnessFetcher) FetchMailboxWithUIDValidity(
	ctx context.Context,
	account store.MailAccount,
	_ string,
	_ uint32,
	expectedUIDValidity uint32,
	_ func(FetchedMessage) error,
) error {
	if account.ID != f.healthyAccountID || expectedUIDValidity != 1 {
		return fmt.Errorf("unexpected mailbox fetch account=%d validity=%d", account.ID, expectedUIDValidity)
	}
	f.healthyOnce.Do(func() { close(f.healthyStarted) })
	select {
	case <-f.releaseHealthy:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *boundedRecoveryFairnessFetcher) FetchMailbox(
	ctx context.Context,
	account store.MailAccount,
	mailbox string,
	afterUID uint32,
	handle func(FetchedMessage) error,
) error {
	status, err := f.MailboxStatus(ctx, account, mailbox)
	if err != nil {
		return err
	}
	return f.FetchMailboxWithUIDValidity(ctx, account, mailbox, afterUID, status.UIDValidity, handle)
}

func TestBoundedGenerationRecoveryRunsHealthyAccountBeforeNextBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "bounded-recovery-fairness@example.test", "Fair Recovery", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAccount := recoveryTestAccount(t, ctx, db, user.ID, "bounded-recovery")
	healthyAccount := recoveryTestAccount(t, ctx, db, user.ID, "bounded-healthy")
	recoveryMailbox, err := db.GetOrCreateMailbox(ctx, user.ID, recoveryAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	healthyMailbox, err := db.GetOrCreateMailbox(ctx, user.ID, healthyAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, recoveryMailbox.ID, 600, 0, 601, 2); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxRemoteStatus(ctx, user.ID, healthyMailbox.ID, 0, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	insertRecoveryTestMarker(t, ctx, db, user.ID, recoveryAccount.ID, recoveryMailbox.ID, 2)

	messages := make([]FetchedMessage, 600)
	for i := range messages {
		uid := uint32(i + 1)
		messages[i] = FetchedMessage{
			Mailbox: "INBOX", UID: uid, UIDValidity: 2, InternalDate: time.Now().UTC(),
			Raw: []byte(fmt.Sprintf("Message-ID: <fair-recovery-%d@example.test>\r\nFrom: Sender <sender@example.test>\r\nSubject: Recovery %d\r\n\r\nbody\r\n", uid, uid)),
		}
	}
	fetcher := &boundedRecoveryFairnessFetcher{
		moveTestFetcher:   &moveTestFetcher{},
		recoveryAccountID: recoveryAccount.ID,
		healthyAccountID:  healthyAccount.ID,
		messages:          messages,
		historyStarted:    make(chan struct{}),
		releaseHistory:    make(chan struct{}),
		healthyStarted:    make(chan struct{}),
		releaseHealthy:    make(chan struct{}),
	}
	service := &Service{Store: db, Blobs: blob.New(dir), Fetcher: fetcher}
	runner := NewRunnerWithContext(ctx, service)
	runner.rebuildRecoveryInterval = time.Hour
	nextAttemptObserved := make(chan bool, 1)
	runner.queueRebuildMailbox = func(rebuild store.PendingMailboxGenerationRebuild) {
		if rebuild.AccountID != recoveryAccount.ID {
			return
		}
		select {
		case nextAttemptObserved <- runner.IsAccountMailboxRunning(user.ID, healthyAccount.ID, healthyMailbox.Name):
		default:
		}
	}
	rebuilds, err := db.ListPendingMailboxGenerationRebuilds(ctx)
	if err != nil || len(rebuilds) != 1 {
		t.Fatalf("pending rebuilds=%+v err=%v", rebuilds, err)
	}
	if !runner.startPendingMailboxGenerationRebuild(rebuilds[0]) {
		t.Fatal("failed to start bounded generation recovery")
	}
	select {
	case <-fetcher.historyStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first recovery history batch did not start")
	}
	if !runner.QueueAccountMailboxes(user.ID, healthyAccount.ID, []string{healthyMailbox.Name}) {
		t.Fatal("healthy Inbox refresh was not accepted for deferral")
	}
	if runner.IsAccountMailboxRunning(user.ID, healthyAccount.ID, healthyMailbox.Name) {
		t.Fatal("healthy account started before the active recovery batch released")
	}
	close(fetcher.releaseHistory)
	select {
	case healthyReserved := <-nextAttemptObserved:
		if !healthyReserved {
			t.Fatal("next recovery attempt was considered before the deferred healthy account reserved Inbox")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bounded recovery did not release and rescan pending work")
	}
	select {
	case <-fetcher.healthyStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("deferred healthy account sync did not start")
	}
	close(fetcher.releaseHealthy)
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runner.mu.Lock()
		recoveryRunning := runner.generationRecoveryRuns[user.ID] || runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !recoveryRunning && !runner.IsAccountMailboxRunning(user.ID, healthyAccount.ID, healthyMailbox.Name) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner did not stop after fairness test cancellation")
		}
		time.Sleep(time.Millisecond)
	}
}
