package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/store"
)

type recoveryHealthyAccountFetcher struct {
	*moveTestFetcher
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type recoveryFailingFetcher struct {
	*moveTestFetcher
	calls atomic.Int64
}

type recoveryBlockingFetcher struct {
	*moveTestFetcher
	started chan struct{}
	once    sync.Once
}

func (f *recoveryFailingFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	f.calls.Add(1)
	return MailboxStatus{}, errors.New("recovery IMAP unavailable")
}

func (f *recoveryBlockingFetcher) MailboxStatus(ctx context.Context, _ store.MailAccount, _ string) (MailboxStatus, error) {
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	return MailboxStatus{}, ctx.Err()
}

func (f *recoveryHealthyAccountFetcher) ListMailboxes(context.Context, store.MailAccount) ([]MailboxInfo, error) {
	return []MailboxInfo{{Name: "INBOX"}}, nil
}

func (f *recoveryHealthyAccountFetcher) MailboxStatus(context.Context, store.MailAccount, string) (MailboxStatus, error) {
	return MailboxStatus{UIDNext: 1, UIDValidity: 1}, nil
}

func (f *recoveryHealthyAccountFetcher) FetchMailbox(ctx context.Context, _ store.MailAccount, _ string, _ uint32, _ func(FetchedMessage) error) error {
	f.once.Do(func() { close(f.started) })
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *recoveryHealthyAccountFetcher) FetchMailboxWithUIDValidity(ctx context.Context, account store.MailAccount,
	mailbox string, afterUID, expectedUIDValidity uint32, handle func(FetchedMessage) error,
) error {
	if expectedUIDValidity != 1 {
		return errors.New("unexpected recovery test UIDVALIDITY")
	}
	return f.FetchMailbox(ctx, account, mailbox, afterUID, handle)
}

func (f *recoveryHealthyAccountFetcher) FetchUIDsWithUIDValidity(context.Context, store.MailAccount,
	string, []uint32, uint32, func(FetchedMessage) error,
) error {
	return nil
}

func TestRequestedNeverMailboxBypassRequiresExactRebuildMarker(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-never@example.test", "Rebuild Never", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	firstAccount := recoveryTestAccount(t, ctx, db, user.ID, "first")
	secondAccount := recoveryTestAccount(t, ctx, db, user.ID, "second")
	firstMailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, firstAccount.ID, "INBOX")
	recoveryTestNeverMailbox(t, ctx, db, user.ID, secondAccount.ID, "INBOX")
	service := &Service{Store: db}

	mailboxes, err := service.requestedMailboxes(ctx, firstAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 0 {
		t.Fatalf("never mailbox without marker was requested: %v", mailboxes)
	}
	insertRecoveryTestMarker(t, ctx, db, user.ID, firstAccount.ID, firstMailbox.ID, 42)
	mailboxes, err = service.requestedMailboxes(ctx, firstAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 1 || mailboxes[0] != "INBOX" {
		t.Fatalf("marked never mailbox request=%v, want INBOX", mailboxes)
	}
	mailboxes, err = service.requestedMailboxes(ctx, secondAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 0 {
		t.Fatalf("marker crossed into same-named mailbox on another account: %v", mailboxes)
	}

	other, err := db.CreateUser(ctx, "rebuild-never-other@example.test", "Other Rebuild", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount := recoveryTestAccount(t, ctx, db, other.ID, "other")
	recoveryTestNeverMailbox(t, ctx, db, other.ID, otherAccount.ID, "INBOX")
	mailboxes, err = service.requestedMailboxes(ctx, otherAccount, []string{"INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mailboxes) != 0 {
		t.Fatalf("marker crossed tenant boundary: %v", mailboxes)
	}
}

func TestMailboxGenerationRecoveryRetriesUntilMarkerClears(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-retry@example.test", "Rebuild Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "retry")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 77)
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.rebuildRecoveryInterval = 5 * time.Millisecond
	queued := make(chan store.PendingMailboxGenerationRebuild, 4)
	result := make(chan error, 1)
	var calls atomic.Int64
	runner.queueRebuildMailbox = func(rebuild store.PendingMailboxGenerationRebuild) {
		call := calls.Add(1)
		queued <- rebuild
		if call == 2 {
			result <- db.FinalizeMailboxGenerationRebuild(ctx, rebuild.UserID, rebuild.AccountID,
				rebuild.MailboxID, rebuild.TargetUIDValidity)
		}
	}
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	first := <-queued
	if first.UserID != user.ID || first.AccountID != account.ID || first.MailboxID != mailbox.ID {
		t.Fatalf("initial recovery queue=%+v, want exact marker", first)
	}
	select {
	case second := <-queued:
		if second.UserID != user.ID || second.AccountID != account.ID || second.MailboxID != mailbox.ID {
			t.Fatalf("retry recovery queue=%+v, want exact marker", second)
		}
	case <-time.After(time.Second):
		t.Fatal("failed recovery pass was not retried")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		pending, err := db.MailboxGenerationRebuildExists(ctx, user.ID, account.ID, mailbox.ID)
		if err != nil {
			t.Fatal(err)
		}
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !pending && !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery did not stop after marker cleared: pending=%v running=%v", pending, running)
		}
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("recovery queue calls=%d, want failed pass plus one retry", got)
	}
}

func TestMailboxGenerationRecoveryFailureWaitsForRetryInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-backoff@example.test", "Rebuild Backoff", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "backoff")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 79)
	fetcher := &recoveryFailingFetcher{moveTestFetcher: &moveTestFetcher{}}
	runner := NewRunnerWithContext(ctx, &Service{Store: db, Fetcher: fetcher})
	runner.rebuildRecoveryInterval = 200 * time.Millisecond
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for fetcher.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("initial recovery attempt did not run")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fetcher.calls.Load(); got != 1 {
		t.Fatalf("failed recovery retried immediately: calls=%d", got)
	}
	deadline = time.Now().Add(time.Second)
	for fetcher.calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("failed recovery did not retry on the timer: calls=%d", fetcher.calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMailboxGenerationRecoveryDeadlineReleasesReservationAndKeepsMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-deadline@example.test", "Rebuild Deadline", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "deadline")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 91)
	fetcher := &recoveryBlockingFetcher{moveTestFetcher: &moveTestFetcher{}, started: make(chan struct{})}
	runner := NewRunnerWithContext(ctx, &Service{Store: db, Fetcher: fetcher})
	runner.generationRecoveryTimeout = 25 * time.Millisecond
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID:            user.ID,
		AccountID:         account.ID,
		MailboxID:         mailbox.ID,
		MailboxName:       mailbox.Name,
		TargetUIDValidity: 91,
	}
	if !runner.startPendingMailboxGenerationRebuild(rebuild) {
		t.Fatal("generation recovery did not start")
	}
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("generation recovery did not reach IMAP status")
	}

	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		running := runner.generationRecoveryRuns[user.ID]
		runner.mu.Unlock()
		if !running && !runner.IsAccountMailboxRunning(user.ID, account.ID, mailbox.Name) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed-out generation recovery did not release its reservation")
		}
		time.Sleep(time.Millisecond)
	}
	pending, err := db.MailboxGenerationRebuildPending(ctx, user.ID, account.ID, mailbox.ID, 91)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("timed-out generation recovery removed its durable rebuild marker")
	}
}

func TestMailboxGenerationRecoveryQueuesOneInboxFirstPerUser(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	first, err := db.CreateUser(ctx, "rebuild-priority-first@example.test", "First", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	firstHistoryAccount := recoveryTestAccount(t, ctx, db, first.ID, "priority-first-history")
	firstHistory := recoveryTestNeverMailbox(t, ctx, db, first.ID, firstHistoryAccount.ID, "Archive")
	firstInboxAccount := recoveryTestAccount(t, ctx, db, first.ID, "priority-first-inbox")
	firstInbox := recoveryTestNeverMailbox(t, ctx, db, first.ID, firstInboxAccount.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, first.ID, firstHistoryAccount.ID, firstHistory.ID, 101)
	insertRecoveryTestMarker(t, ctx, db, first.ID, firstInboxAccount.ID, firstInbox.ID, 102)

	second, err := db.CreateUser(ctx, "rebuild-priority-second@example.test", "Second", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	secondAccount := recoveryTestAccount(t, ctx, db, second.ID, "priority-second")
	secondHistory := recoveryTestNeverMailbox(t, ctx, db, second.ID, secondAccount.ID, "History")
	secondInbox := recoveryTestNeverMailbox(t, ctx, db, second.ID, secondAccount.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, second.ID, secondAccount.ID, secondHistory.ID, 201)
	insertRecoveryTestMarker(t, ctx, db, second.ID, secondAccount.ID, secondInbox.ID, 202)

	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	var queued []store.PendingMailboxGenerationRebuild
	runner.queueRebuildMailbox = func(rebuild store.PendingMailboxGenerationRebuild) {
		queued = append(queued, rebuild)
	}
	pending, err := runner.queuePendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 4 {
		t.Fatalf("pending rebuilds=%d, want 4", pending)
	}
	assertQueuedRecoveryMailboxes(t, queued, []store.Mailbox{firstInbox, secondInbox})

	for _, rebuild := range queued {
		if err := db.FinalizeMailboxGenerationRebuild(ctx, rebuild.UserID, rebuild.AccountID,
			rebuild.MailboxID, rebuild.TargetUIDValidity); err != nil {
			t.Fatal(err)
		}
	}
	queued = nil
	pending, err = runner.queuePendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 2 {
		t.Fatalf("remaining rebuilds=%d, want 2", pending)
	}
	assertQueuedRecoveryMailboxes(t, queued, []store.Mailbox{firstHistory, secondHistory})
}

func TestMailboxGenerationRecoveryRotatesFailingAccountAndReopensRecoveredAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-rotation@example.test", "Rotation", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	failingAccount := recoveryTestAccount(t, ctx, db, user.ID, "rotation-failing")
	failingInbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, failingAccount.ID, "INBOX")
	healthyAccount := recoveryTestAccount(t, ctx, db, user.ID, "rotation-healthy")
	healthyInbox, err := db.GetOrCreateMailbox(ctx, user.ID, healthyAccount.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSyncMode(ctx, user.ID, healthyInbox.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	insertRecoveryTestMarker(t, ctx, db, user.ID, failingAccount.ID, failingInbox.ID, 801)
	insertRecoveryTestMarker(t, ctx, db, user.ID, healthyAccount.ID, healthyInbox.ID, 802)

	fetcher := &recoveryHealthyAccountFetcher{
		moveTestFetcher: &moveTestFetcher{},
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	runner := NewRunnerWithContext(ctx, &Service{Store: db, Fetcher: fetcher})
	var attempted []store.PendingMailboxGenerationRebuild
	runner.queueRebuildMailbox = func(rebuild store.PendingMailboxGenerationRebuild) {
		attempted = append(attempted, rebuild)
	}

	if _, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil {
		t.Fatal(err)
	}
	if len(attempted) != 2 || attempted[0].AccountID != failingAccount.ID || attempted[1].AccountID != healthyAccount.ID {
		t.Fatalf("recovery attempts=%+v, want failing account then healthy account", attempted)
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, user.ID, healthyAccount.ID, healthyInbox.ID, 802); err != nil {
		t.Fatal(err)
	}

	activeKeys, reserved := runner.reserveGenerationRecoveryMailbox(attempted[0])
	if !reserved {
		t.Fatal("failed account recovery could not reserve the tenant writer")
	}
	if runner.QueueAccountMailboxes(user.ID, healthyAccount.ID, []string{healthyInbox.Name}) != true {
		t.Fatal("healthy account refresh was not accepted for deferral")
	}
	if _, overlapping := runner.reserveGenerationRecoveryMailbox(attempted[1]); overlapping {
		t.Fatal("two generation recoveries reserved the same tenant concurrently")
	}
	runner.releaseGenerationRecoveryMailbox(user.ID, activeKeys)

	if _, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil {
		t.Fatal(err)
	}
	if len(attempted) != 3 || attempted[2].AccountID != failingAccount.ID {
		t.Fatalf("post-recovery attempts=%+v, want rotation back to the failed account", attempted)
	}
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("deferred healthy-account sync did not start after its marker cleared")
	}
	if !runner.IsAccountMailboxRunning(user.ID, healthyAccount.ID, healthyInbox.Name) {
		t.Fatal("healthy account sync did not retain its reservation")
	}
	if _, allowed := runner.reserveAccountMailboxes(user.ID, failingAccount.ID, []string{failingInbox.Name}); allowed {
		t.Fatal("failed account reopened while its marker remained")
	}
	if _, allowed := runner.reserveMailboxes(user.ID, []string{"INBOX"}); allowed {
		t.Fatal("global sync reopened while a tenant marker remained")
	}
	if !runner.MailboxGenerationRecoveryActive(user.ID) {
		t.Fatal("remote-plugin/user recovery gate opened while a marker remained")
	}

	close(fetcher.release)
	deadline := time.Now().Add(time.Second)
	for runner.IsAccountMailboxRunning(user.ID, healthyAccount.ID, healthyInbox.Name) {
		if time.Now().After(deadline) {
			t.Fatal("healthy account sync did not release its reservation")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMailboxGenerationRecoveryStaleScanKeepsAccountsFailClosed(t *testing.T) {
	runner := NewRunnerWithContext(context.Background(), nil)
	const userID = int64(91)
	const oldAccountID = int64(101)
	const newAccountID = int64(202)

	runner.SignalMailboxGenerationRecovery(userID)
	staleSnapshot := runner.generationRecoveryEpochSnapshot()
	runner.SignalMailboxGenerationRecovery(userID)
	runner.reconcileGenerationRecoveryUsers(
		map[int64]bool{userID: true},
		map[int64]map[int64]bool{userID: {oldAccountID: true}},
		staleSnapshot,
	)

	runner.mu.Lock()
	known := runner.generationRecoveryKnown[userID]
	runner.mu.Unlock()
	_, oldAllowed := runner.reserveAccountMailboxes(userID, oldAccountID, []string{"INBOX"})
	_, newAllowed := runner.reserveAccountMailboxes(userID, newAccountID, []string{"INBOX"})
	if known || oldAllowed || newAllowed {
		t.Fatalf("stale scan known=%t old_allowed=%t new_allowed=%t, want fail-closed", known, oldAllowed, newAllowed)
	}
}

func TestMailboxGenerationRecoveryDrainsOneHealthyAccountPerUser(t *testing.T) {
	runner := NewRunnerWithContext(context.Background(), nil)
	const userID = int64(92)
	const pendingAccountID = int64(100)
	runner.mu.Lock()
	runner.generationRecoveryUsers[userID] = true
	runner.generationRecoveryKnown[userID] = true
	runner.generationRecoveryAccounts[userID] = map[int64]bool{pendingAccountID: true}
	runner.deferGenerationRecoveryAccountMailboxesLocked(userID, 200, []string{"INBOX"})
	runner.deferGenerationRecoveryAccountMailboxesLocked(userID, 300, []string{"INBOX"})
	runner.mu.Unlock()

	ready := runner.takeReadyGenerationRecoveryAccountReplays()
	if len(ready) != 1 {
		t.Fatalf("ready account replays=%+v, want exactly one tenant writer", ready)
	}
	runner.mu.Lock()
	remaining := len(runner.generationRecoveryAccts[userID])
	runner.mailboxRunning[accountMailboxKey(userID, ready[0].request.accountID, ready[0].request.mailbox)] = true
	runner.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("deferred account requests remaining=%d, want 1", remaining)
	}
	if more := runner.takeReadyGenerationRecoveryAccountReplays(); len(more) != 0 {
		t.Fatalf("drained another account while ordinary tenant work was active: %+v", more)
	}
}

func TestMailboxGenerationRecoveryAllowsOnlyOneHealthyAccountWriter(t *testing.T) {
	runner := NewRunnerWithContext(context.Background(), nil)
	const userID = int64(93)
	const failedAccountID = int64(100)
	const healthyAccountID = int64(200)
	runner.mu.Lock()
	runner.generationRecoveryUsers[userID] = true
	runner.generationRecoveryKnown[userID] = true
	runner.generationRecoveryAccounts[userID] = map[int64]bool{failedAccountID: true}
	runner.mu.Unlock()

	healthyKeys, reserved := runner.reserveAccountMailboxes(userID, healthyAccountID, []string{"INBOX"})
	if !reserved {
		t.Fatal("first marker-free account mailbox did not reserve")
	}
	if _, overlapping := runner.reserveAccountMailboxes(userID, healthyAccountID, []string{"Archive"}); overlapping {
		t.Fatal("second healthy mailbox overlapped the tenant writer")
	}
	failedRebuild := store.PendingMailboxGenerationRebuild{
		UserID: userID, AccountID: failedAccountID, MailboxID: 300, MailboxName: "INBOX", TargetUIDValidity: 44,
	}
	if _, overlapping := runner.reserveGenerationRecoveryMailbox(failedRebuild); overlapping {
		t.Fatal("generation recovery overlapped healthy-account sync")
	}
	runner.releaseAccountMailboxReservations(userID, healthyAccountID, []string{"INBOX"}, healthyKeys)
	recoveryKeys, recovered := runner.reserveGenerationRecoveryMailbox(failedRebuild)
	if !recovered {
		t.Fatal("generation recovery did not reserve after healthy-account sync ended")
	}
	runner.releaseGenerationRecoveryMailbox(userID, recoveryKeys)
}

func TestMailboxGenerationRecoveryPollingDoesNotQueueBehindActiveMailbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-active@example.test", "Active", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "active")
	inbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	history := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "History")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, inbox.ID, 301)
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, history.ID, 302)

	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.rebuildRecoveryInterval = 5 * time.Millisecond
	activeRebuild := store.PendingMailboxGenerationRebuild{
		UserID: user.ID, AccountID: account.ID, MailboxID: inbox.ID,
		MailboxName: inbox.Name, TargetUIDValidity: 301,
	}
	keys, reserved := runner.reserveGenerationRecoveryMailbox(activeRebuild)
	if !reserved {
		t.Fatal("failed to reserve the active Inbox recovery")
	}
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	// Several accelerated recovery passes represent an Inbox rebuild that stays
	// active beyond the production 30-second polling interval.
	time.Sleep(40 * time.Millisecond)
	runner.mu.Lock()
	inboxPending := runner.accountMailboxPending[accountMailboxKey(user.ID, account.ID, inbox.Name)]
	historyPending := runner.accountMailboxPending[accountMailboxKey(user.ID, account.ID, history.Name)]
	historyRunning := runner.accountMailboxReservedLocked(user.ID, account.ID, history.Name)
	recoveryDeferred := len(runner.generationRecoveryAccts[user.ID])
	runner.mu.Unlock()
	if inboxPending || historyPending || historyRunning || recoveryDeferred != 0 {
		t.Fatalf("active recovery queued redundant work: inbox_pending=%t history_pending=%t history_running=%t deferred=%d",
			inboxPending, historyPending, historyRunning, recoveryDeferred)
	}

	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		recoveryLoopRunning := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !recoveryLoopRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery polling did not stop after cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	runner.releaseGenerationRecoveryMailbox(user.ID, keys)
}

func TestMailboxGenerationRecoveryGateDefersTenantWorkUntilFinalMarker(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUser(ctx, "rebuild-gate@example.test", "Gate Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	firstAccount := recoveryTestAccount(t, ctx, db, owner.ID, "gate-first")
	firstMailbox := recoveryTestNeverMailbox(t, ctx, db, owner.ID, firstAccount.ID, "INBOX")
	secondAccount := recoveryTestAccount(t, ctx, db, owner.ID, "gate-second")
	secondMailbox := recoveryTestNeverMailbox(t, ctx, db, owner.ID, secondAccount.ID, "History")
	blockedAccount := recoveryTestAccount(t, ctx, db, owner.ID, "gate-blocked")
	blockedMailbox := recoveryTestNeverMailbox(t, ctx, db, owner.ID, blockedAccount.ID, "MixedCase Folder")
	insertRecoveryTestMarker(t, ctx, db, owner.ID, firstAccount.ID, firstMailbox.ID, 501)
	insertRecoveryTestMarker(t, ctx, db, owner.ID, secondAccount.ID, secondMailbox.ID, 502)

	other, err := db.CreateUser(ctx, "rebuild-gate-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount := recoveryTestAccount(t, ctx, db, other.ID, "gate-other")
	otherMailbox := recoveryTestNeverMailbox(t, ctx, db, other.ID, otherAccount.ID, "INBOX")

	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.queueRebuildMailbox = func(store.PendingMailboxGenerationRebuild) {}
	replayed := make(chan generationRecoveryReplay, 1)
	runner.replayGenerationRecovery = func(replay generationRecoveryReplay) {
		replayed <- replay
	}
	if pending, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil || pending != 2 {
		t.Fatalf("initial recovery queue pending=%d err=%v", pending, err)
	}
	healthyKeys, healthyReserved := runner.reserveAccountMailboxes(owner.ID, blockedAccount.ID, []string{blockedMailbox.Name})
	if !healthyReserved {
		t.Fatal("marker-free account was blocked by another account's recovery marker")
	}
	runner.releaseAccountMailboxReservations(owner.ID, blockedAccount.ID, []string{blockedMailbox.Name}, healthyKeys)
	if _, started, err := runner.StartMailboxMaintenance(owner.ID, blockedMailbox, "Healthy maintenance", func(context.Context, int64, *store.SyncProgress) error {
		t.Fatal("maintenance callback ran while another account was recovering")
		return nil
	}); err != nil || started {
		t.Fatalf("healthy-account maintenance bypassed broad recovery gate: started=%t err=%v", started, err)
	}
	runner.mu.Lock()
	firstAccountBlocked := runner.generationRecoveryAccountGatedLocked(owner.ID, firstAccount.ID)
	runner.mu.Unlock()
	if !firstAccountBlocked {
		t.Fatal("account with a pending recovery marker was allowed to sync")
	}
	if runner.StartMailboxes(owner.ID, []string{blockedMailbox.Name}) {
		t.Fatal("global mailbox sync started while tenant recovery was held")
	}
	if _, started, err := runner.StartMailboxMaintenance(owner.ID, firstMailbox, "Purging", func(context.Context, int64, *store.SyncProgress) error {
		t.Fatal("maintenance callback ran during generation recovery")
		return nil
	}); err != nil || started {
		t.Fatalf("maintenance during recovery started=%t err=%v", started, err)
	}
	runner.RefreshSenderStats(owner.ID)
	if runner.StartAttachmentIndex(owner.ID) {
		t.Fatal("attachment indexing started during generation recovery")
	}

	otherKeys, ok := runner.reserveAccountMailboxes(other.ID, otherAccount.ID, []string{otherMailbox.Name})
	if !ok {
		t.Fatal("another tenant could not make progress while owner recovery was held")
	}
	runner.releaseAccountMailboxReservations(other.ID, otherAccount.ID, []string{otherMailbox.Name}, otherKeys)

	if err := db.FinalizeMailboxGenerationRebuild(ctx, owner.ID, firstAccount.ID, firstMailbox.ID, 501); err != nil {
		t.Fatal(err)
	}
	if pending, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil || pending != 1 {
		t.Fatalf("second recovery queue pending=%d err=%v", pending, err)
	}
	firstKeys, firstReserved := runner.reserveAccountMailboxes(owner.ID, firstAccount.ID, []string{firstMailbox.Name})
	if !firstReserved {
		t.Fatal("recovered account did not reopen while another account marker remained")
	}
	runner.releaseAccountMailboxReservations(owner.ID, firstAccount.ID, []string{firstMailbox.Name}, firstKeys)
	runner.mu.Lock()
	secondAccountBlocked := runner.generationRecoveryAccountGatedLocked(owner.ID, secondAccount.ID)
	runner.mu.Unlock()
	if !secondAccountBlocked {
		t.Fatal("account with the remaining recovery marker was allowed to sync")
	}
	select {
	case replay := <-replayed:
		t.Fatalf("tenant work replayed between two recovery markers: %+v", replay)
	default:
	}
	runner.mu.Lock()
	stillGated := runner.generationRecoveryUsers[owner.ID]
	senderStatsPending := runner.senderStatsPending[owner.ID]
	attachmentPending := runner.attachmentPending[owner.ID]
	attachmentRunning := runner.mailboxRunning[mailboxKey(owner.ID, "__attachments__")]
	runner.mu.Unlock()
	if !stillGated || !senderStatsPending || !attachmentPending || attachmentRunning {
		t.Fatalf("between-marker gate=%t sender_pending=%t attachment_pending=%t attachment_running=%t",
			stillGated, senderStatsPending, attachmentPending, attachmentRunning)
	}

	if err := db.FinalizeMailboxGenerationRebuild(ctx, owner.ID, secondAccount.ID, secondMailbox.ID, 502); err != nil {
		t.Fatal(err)
	}
	if pending, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil || pending != 0 {
		t.Fatalf("final recovery queue pending=%d err=%v", pending, err)
	}
	select {
	case replay := <-replayed:
		if replay.userID != owner.ID || replay.auto || len(replay.mailboxes) != 1 ||
			replay.mailboxes[0] != blockedMailbox.Name || len(replay.accountMailboxes) != 0 ||
			!replay.senderStats || !replay.attachments {
			t.Fatalf("replayed tenant work=%+v", replay)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred tenant work was not replayed after the final marker")
	}
}

func TestMailboxGenerationRecoveryRefreshCannotClearAnotherTenantGate(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := db.CreateUser(ctx, "rebuild-refresh-first@example.test", "First", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	firstAccount := recoveryTestAccount(t, ctx, db, first.ID, "refresh-first")
	firstMailbox := recoveryTestNeverMailbox(t, ctx, db, first.ID, firstAccount.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, first.ID, firstAccount.ID, firstMailbox.ID, 601)
	second, err := db.CreateUser(ctx, "rebuild-refresh-second@example.test", "Second", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	secondAccount := recoveryTestAccount(t, ctx, db, second.ID, "refresh-second")
	secondMailbox := recoveryTestNeverMailbox(t, ctx, db, second.ID, secondAccount.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, second.ID, secondAccount.ID, secondMailbox.ID, 602)

	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.queueRebuildMailbox = func(store.PendingMailboxGenerationRebuild) {}
	runner.replayGenerationRecovery = func(generationRecoveryReplay) {}
	if pending, err := runner.queuePendingMailboxGenerationRebuilds(ctx); err != nil || pending != 2 {
		t.Fatalf("initial recovery queue pending=%d err=%v", pending, err)
	}
	if err := db.FinalizeMailboxGenerationRebuild(ctx, first.ID, firstAccount.ID, firstMailbox.ID, 601); err != nil {
		t.Fatal(err)
	}
	runner.refreshGenerationRecoveryGateForUser(ctx, first.ID)
	runner.mu.Lock()
	firstGated := runner.generationRecoveryUsers[first.ID]
	secondGated := runner.generationRecoveryUsers[second.ID]
	runner.mu.Unlock()
	if firstGated || !secondGated {
		t.Fatalf("targeted refresh gates first=%t second=%t, want false/true", firstGated, secondGated)
	}
}

func TestMailboxGenerationRecoverySignalWakesAndStopsEmptyLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-signal-empty@example.test", "Signal", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.replayGenerationRecovery = func(generationRecoveryReplay) {}
	runner.SignalMailboxGenerationRecovery(user.ID)
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		loopRunning := runner.rebuildRecoveryRunning
		gated := runner.generationRecoveryUsers[user.ID]
		runner.mu.Unlock()
		if !loopRunning && !gated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("empty recovery signal did not settle: loop_running=%t gated=%t", loopRunning, gated)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMailboxGenerationRecoverySignalDuringLoopStopIsNotLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-lost-wake@example.test", "Lost Wake", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "lost-wake")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.rebuildRecoveryInterval = time.Hour
	reachedStop := make(chan struct{})
	releaseStop := make(chan struct{})
	var once sync.Once
	runner.rebuildRecoveryBeforeStop = func() {
		once.Do(func() {
			close(reachedStop)
			<-releaseStop
		})
	}
	queued := make(chan store.PendingMailboxGenerationRebuild, 1)
	runner.queueRebuildMailbox = func(rebuild store.PendingMailboxGenerationRebuild) {
		queued <- rebuild
	}
	runner.wakeMailboxGenerationRebuildRecovery()
	select {
	case <-reachedStop:
	case <-time.After(time.Second):
		t.Fatal("empty recovery loop did not reach its stop boundary")
	}
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 701)
	if rebuilds, err := db.ListPendingMailboxGenerationRebuilds(ctx); err != nil || len(rebuilds) != 1 {
		t.Fatalf("inserted successor marker rebuilds=%+v err=%v", rebuilds, err)
	}
	runner.SignalMailboxGenerationRecovery(user.ID)
	close(releaseStop)
	select {
	case rebuild := <-queued:
		if rebuild.UserID != user.ID || rebuild.AccountID != account.ID || rebuild.MailboxID != mailbox.ID {
			t.Fatalf("successor recovery queued %+v", rebuild)
		}
	case <-time.After(3 * time.Second):
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		gated := runner.generationRecoveryUsers[user.ID]
		wakeCount := len(runner.rebuildRecoveryWake)
		runner.mu.Unlock()
		rebuilds, queryErr := db.ListPendingMailboxGenerationRebuilds(ctx)
		t.Fatalf("recovery signal was lost while the prior loop stopped: running=%t gated=%t wake=%d rebuilds=%+v err=%v",
			running, gated, wakeCount, rebuilds, queryErr)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery loop did not stop after cancellation")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMailboxGenerationRecoveryWaitsForCanceledAttachmentWorkerExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-attachment-exit@example.test", "Attachment Exit", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	replayed := make(chan generationRecoveryReplay, 1)
	runner.replayGenerationRecovery = func(replay generationRecoveryReplay) {
		replayed <- replay
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	key := mailboxKey(user.ID, "__attachments__")
	runner.mu.Lock()
	runner.mailboxRunning[key] = true
	runner.attachmentCancels[user.ID] = stopWorker
	runner.attachmentDone[user.ID] = done
	runner.mu.Unlock()

	runner.SignalMailboxGenerationRecovery(user.ID)
	select {
	case <-workerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("generation recovery did not cancel the existing attachment worker")
	}
	time.Sleep(20 * time.Millisecond)
	if !runner.MailboxGenerationRecoveryActive(user.ID) {
		t.Fatal("recovery gate opened before the canceled attachment worker exited")
	}
	select {
	case replay := <-replayed:
		t.Fatalf("replay started before the canceled attachment worker exited: %+v", replay)
	default:
	}

	runner.mu.Lock()
	delete(runner.mailboxRunning, key)
	delete(runner.attachmentCancels, user.ID)
	delete(runner.attachmentDone, user.ID)
	runner.mu.Unlock()
	close(done)
	runner.refreshGenerationRecoveryGateForUser(ctx, user.ID)
	select {
	case replay := <-replayed:
		if replay.userID != user.ID || !replay.attachments {
			t.Fatalf("post-worker replay=%+v", replay)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not replay after the canceled attachment worker exited")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery loop did not stop after attachment-worker test cancellation")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMailboxGenerationRecoveryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "rebuild-cancel@example.test", "Rebuild Cancel", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account := recoveryTestAccount(t, ctx, db, user.ID, "cancel")
	mailbox := recoveryTestNeverMailbox(t, ctx, db, user.ID, account.ID, "INBOX")
	insertRecoveryTestMarker(t, ctx, db, user.ID, account.ID, mailbox.ID, 88)
	runner := NewRunnerWithContext(ctx, &Service{Store: db})
	runner.rebuildRecoveryInterval = time.Hour
	var calls atomic.Int64
	runner.queueRebuildMailbox = func(store.PendingMailboxGenerationRebuild) {
		calls.Add(1)
	}
	if err := runner.RecoverPendingInboxArrivals(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("initial recovery calls=%d, want one", calls.Load())
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		runner.mu.Lock()
		running := runner.rebuildRecoveryRunning
		runner.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rebuild recovery loop ignored cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("recovery queued after cancellation: calls=%d", calls.Load())
	}
}

func recoveryTestAccount(t *testing.T, ctx context.Context, db *store.Store, userID int64, suffix string) store.MailAccount {
	t.Helper()
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: userID, Email: suffix + "@example.test", Host: "imap.example.test", Port: 993,
		Username: suffix, EncryptedPassword: "encrypted", UseTLS: true, Mailbox: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func recoveryTestNeverMailbox(t *testing.T, ctx context.Context, db *store.Store, userID, accountID int64, name string) store.Mailbox {
	t.Helper()
	mailbox, err := db.GetOrCreateMailbox(ctx, userID, accountID, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateMailboxSyncMode(ctx, userID, mailbox.ID, "never"); err != nil {
		t.Fatal(err)
	}
	mailbox.SyncMode = "never"
	return mailbox
}

func insertRecoveryTestMarker(t *testing.T, ctx context.Context, db *store.Store,
	userID, accountID, mailboxID int64, targetUIDValidity uint32,
) {
	t.Helper()
	userDB, err := db.UserDB(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err := userDB.ExecContext(ctx, `INSERT INTO mailbox_generation_rebuilds
		(user_id, account_id, mailbox_id, target_uid_validity, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, userID, accountID, mailboxID, targetUIDValidity, now, now); err != nil {
		t.Fatal(err)
	}
}

func assertQueuedRecoveryMailboxes(t *testing.T, got []store.PendingMailboxGenerationRebuild, wantMailboxes []store.Mailbox) {
	t.Helper()
	if len(got) != len(wantMailboxes) {
		t.Fatalf("queued rebuilds=%+v, want %d", got, len(wantMailboxes))
	}
	for i, mailbox := range wantMailboxes {
		if got[i].UserID != mailbox.UserID || got[i].AccountID != mailbox.AccountID ||
			got[i].MailboxID != mailbox.ID || got[i].MailboxName != mailbox.Name {
			t.Fatalf("queued rebuild %d=%+v, want mailbox %+v", i, got[i], mailbox)
		}
	}
}
