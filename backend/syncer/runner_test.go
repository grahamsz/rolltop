// File overview: Tests for sync runner mailbox reservation semantics.

package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/store"
)

type autoForegroundPriorityFetcher struct {
	*moveTestFetcher
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
}

func (f *autoForegroundPriorityFetcher) MailboxStatus(ctx context.Context, _ store.MailAccount, mailbox string) (MailboxStatus, error) {
	switch strings.ToLower(strings.TrimSpace(mailbox)) {
	case "inbox":
		select {
		case f.firstStarted <- struct{}{}:
		default:
		}
		select {
		case <-f.releaseFirst:
		case <-ctx.Done():
			return MailboxStatus{}, ctx.Err()
		}
	case "archive":
		select {
		case f.secondStarted <- struct{}{}:
		default:
		}
	}
	return MailboxStatus{UIDNext: 1, UIDValidity: 1}, nil
}

func TestRunnerMailboxReservationsConflictAcrossGlobalAndAccountJobs(t *testing.T) {
	r := NewRunner(nil)
	if _, ok := r.reserveAccountMailboxes(7, 101, []string{"Gmail Forward"}); !ok {
		t.Fatalf("initial account mailbox reservation failed")
	}
	if _, ok := r.reserveMailboxes(7, []string{"gmail forward"}); ok {
		t.Fatalf("global mailbox reservation overlapped an account-specific reservation")
	}
	if _, ok := r.reserveAccountMailboxes(7, 202, []string{"Gmail Forward"}); !ok {
		t.Fatalf("different account should be able to sync the same mailbox name independently")
	}
}

func TestRunnerGlobalMailboxReservationBlocksAccountJob(t *testing.T) {
	r := NewRunner(nil)
	if _, ok := r.reserveMailboxes(7, []string{"Gmail Forward"}); !ok {
		t.Fatalf("initial global mailbox reservation failed")
	}
	if _, ok := r.reserveAccountMailboxes(7, 101, []string{"Gmail Forward"}); ok {
		t.Fatalf("account-specific mailbox reservation overlapped a global reservation")
	}
	if !r.IsMailboxRunning(7, "gmail forward") {
		t.Fatalf("global running state did not report the mailbox as active")
	}
	if !r.IsAccountMailboxRunning(7, 101, "gmail forward") {
		t.Fatalf("account running state did not notice the global mailbox reservation")
	}
}

func TestAccountMailboxBlockReasonNamesActiveGenerationRecovery(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID:      7,
		AccountID:   101,
		MailboxName: "INBOX",
	}
	keys, ok := runner.reserveGenerationRecoveryMailbox(rebuild)
	if !ok {
		t.Fatal("generation recovery reservation failed")
	}
	defer runner.releaseGenerationRecoveryMailbox(rebuild.UserID, keys)

	reason := runner.AccountMailboxBlockReason(rebuild.UserID, rebuild.AccountID, "INBOX")
	for _, want := range []string{"mailbox generation recovery active", "account_id=101", `mailbox="INBOX"`} {
		if !strings.Contains(reason, want) {
			t.Fatalf("block reason %q does not contain %q", reason, want)
		}
	}
}

func TestActiveGenerationRecoveryAllowsUnrelatedAccountMailboxPolls(t *testing.T) {
	runner := NewRunner(nil)
	rebuild := store.PendingMailboxGenerationRebuild{
		UserID:      7,
		AccountID:   101,
		MailboxName: "Gmail Forward",
	}
	keys, ok := runner.reserveGenerationRecoveryMailbox(rebuild)
	if !ok {
		t.Fatal("generation recovery reservation failed")
	}
	defer runner.releaseGenerationRecoveryMailbox(rebuild.UserID, keys)
	runner.mu.Lock()
	runner.generationRecoveryKnown[rebuild.UserID] = true
	runner.generationRecoveryAccounts[rebuild.UserID] = map[int64]bool{rebuild.AccountID: true}
	runner.generationRecoveryTargets[rebuild.UserID] = map[string]bool{
		accountMailboxKey(rebuild.UserID, rebuild.AccountID, rebuild.MailboxName): true,
	}
	runner.mu.Unlock()

	sameAccountKeys, ok := runner.reserveAccountMailboxes(rebuild.UserID, rebuild.AccountID, []string{"INBOX"})
	if !ok {
		t.Fatal("same-account Inbox poll was blocked by unrelated folder recovery")
	}
	if _, ok := runner.reserveAccountMailboxes(rebuild.UserID, 202, []string{"INBOX"}); ok {
		t.Fatal("second Inbox poll overlapped the active recovery and first live Inbox writer")
	}
	if reason := runner.AccountMailboxBlockReason(rebuild.UserID, 202, "INBOX"); !strings.Contains(reason, "another live Inbox sync") {
		t.Fatalf("second Inbox block reason=%q", reason)
	}
	runner.releaseAccountMailboxReservations(rebuild.UserID, rebuild.AccountID, []string{"INBOX"}, sameAccountKeys)

	otherAccountKeys, ok := runner.reserveAccountMailboxes(rebuild.UserID, 202, []string{"INBOX"})
	if !ok {
		t.Fatal("next Inbox poll did not start after the first live writer released")
	}
	runner.releaseAccountMailboxReservations(rebuild.UserID, 202, []string{"INBOX"}, otherAccountKeys)
	if _, ok := runner.reserveAccountMailboxes(rebuild.UserID, 202, []string{"Archive"}); ok {
		t.Fatal("non-Inbox folder bypassed active generation recovery")
	}
	if _, ok := runner.reserveAccountMailboxes(rebuild.UserID, 202, []string{"INBOX", "Archive"}); ok {
		t.Fatal("mixed Inbox/non-Inbox request bypassed active generation recovery")
	}

	if _, ok := runner.reserveAccountMailboxes(rebuild.UserID, rebuild.AccountID, []string{rebuild.MailboxName}); ok {
		t.Fatal("recovery target accepted an overlapping normal sync")
	}
	if reason := runner.AccountMailboxBlockReason(rebuild.UserID, rebuild.AccountID, "INBOX"); strings.Contains(reason, "generation recovery") {
		t.Fatalf("unrelated Inbox block reason blamed generation recovery: %q", reason)
	}
}

func TestGenerationRecoveryReplayAllowsUnrelatedAccountMailboxPolls(t *testing.T) {
	runner := NewRunner(nil)
	const userID = int64(8)
	const accountID = int64(101)
	replayKey := accountMailboxKey(userID, accountID, "Archive")
	runner.mu.Lock()
	runner.generationRecoveryReplay[userID] = true
	runner.mailboxRunning[replayKey] = true
	runner.mu.Unlock()

	if _, ok := runner.reserveAccountMailboxes(userID, accountID, []string{"INBOX"}); ok {
		t.Fatal("Inbox poll overlapped active generation recovery replay work")
	}
	runner.mu.Lock()
	delete(runner.mailboxRunning, replayKey)
	runner.mu.Unlock()
	inboxKeys, ok := runner.reserveAccountMailboxes(userID, accountID, []string{"INBOX"})
	if !ok {
		t.Fatal("Inbox poll remained blocked after replay mailbox work yielded")
	}
	runner.releaseAccountMailboxReservations(userID, accountID, []string{"INBOX"}, inboxKeys)
	if _, ok := runner.reserveAccountMailboxes(userID, accountID, []string{"Archive"}); ok {
		t.Fatal("replay target accepted an overlapping account sync")
	}
	if reason := runner.AccountMailboxBlockReason(userID, accountID, "Archive"); !strings.Contains(reason, "serialized") {
		t.Fatalf("non-Inbox replay block reason=%q, want serialized recovery", reason)
	}
	runner.mu.Lock()
	runner.generationRecoveryUsers[userID] = true
	delete(runner.generationRecoveryKnown, userID)
	runner.mu.Unlock()
	if _, ok := runner.reserveAccountMailboxes(userID, 202, []string{"INBOX"}); ok {
		t.Fatal("replay allowed a sync while a new recovery marker target was still unknown")
	}
}

func TestRunnerAccountReservationReleasesGlobalPendingRerun(t *testing.T) {
	r := NewRunner(nil)
	keys, ok := r.reserveAccountMailboxes(7, 101, []string{"Gmail Forward"})
	if !ok {
		t.Fatalf("initial account mailbox reservation failed")
	}
	r.markPending(7, []string{"Gmail Forward"})
	reruns := r.releaseAccountMailboxReservations(7, 101, []string{"Gmail Forward"}, keys)
	if len(reruns.global) != 1 || reruns.global[0] != "Gmail Forward" || len(reruns.account) != 0 {
		t.Fatalf("reruns = %#v", reruns)
	}
	if r.mailboxPending[mailboxKey(7, "Gmail Forward")] {
		t.Fatalf("pending mailbox key was not cleared")
	}
	if r.IsAccountMailboxRunning(7, 101, "Gmail Forward") {
		t.Fatalf("account mailbox reservation was not released")
	}
}

func TestRunnerPriorityReservationQueuesAtomicallyBehindAccountJob(t *testing.T) {
	r := NewRunner(nil)
	keys, ok := r.reserveAccountMailboxes(7, 101, []string{"INBOX"})
	if !ok {
		t.Fatal("initial account mailbox reservation failed")
	}
	if _, reserved := r.reserveOrQueueMailboxes(7, []string{"inbox"}); reserved {
		t.Fatal("priority mailbox reservation overlapped the account job")
	}
	reruns := r.releaseAccountMailboxReservations(7, 101, []string{"INBOX"}, keys)
	if !reflect.DeepEqual(reruns.global, []string{"INBOX"}) {
		t.Fatalf("global rerun = %#v", reruns.global)
	}
}

func TestRunnerAccountReservationReleasesAccountQualifiedPendingRerun(t *testing.T) {
	r := NewRunner(nil)
	keys, ok := r.reserveAccountMailboxes(7, 101, []string{"INBOX"})
	if !ok {
		t.Fatalf("initial account mailbox reservation failed")
	}
	if !r.QueueAccountMailboxes(7, 101, []string{"inbox"}) {
		t.Fatalf("account mailbox rerun was not queued")
	}
	reruns := r.releaseAccountMailboxReservations(7, 101, []string{"INBOX"}, keys)
	if len(reruns.global) != 0 || len(reruns.account) != 1 || reruns.account[0] != "INBOX" {
		t.Fatalf("reruns = %#v", reruns)
	}
	if r.accountMailboxPending[accountMailboxKey(7, 101, "INBOX")] {
		t.Fatalf("pending account mailbox key was not cleared")
	}
}

func TestGenerationRecoveryReplayCoalescesGlobalAndAccountMailboxRequests(t *testing.T) {
	runner := NewRunner(nil)
	replay := runner.coalesceGenerationRecoveryReplay(generationRecoveryReplay{
		userID:    7,
		mailboxes: []string{"INBOX"},
		accountMailboxes: []deferredAccountMailbox{
			{accountID: 101, mailbox: "inbox"},
			{accountID: 101, mailbox: "Archive"},
			{accountID: 202, mailbox: "Archive"},
		},
	})
	if !reflect.DeepEqual(replay.mailboxes, []string{"INBOX"}) {
		t.Fatalf("global replay mailboxes=%v", replay.mailboxes)
	}
	wantAccounts := []deferredAccountMailbox{
		{accountID: 101, mailbox: "Archive"},
		{accountID: 202, mailbox: "Archive"},
	}
	if !reflect.DeepEqual(replay.accountMailboxes, wantAccounts) {
		t.Fatalf("account replay mailboxes=%v, want %v", replay.accountMailboxes, wantAccounts)
	}
}

func TestGenerationRecoveryReplaySortsInboxFirstAcrossGlobalAndAccountWork(t *testing.T) {
	runner := NewRunner(nil)
	replay := runner.coalesceGenerationRecoveryReplay(generationRecoveryReplay{
		userID:    7,
		mailboxes: []string{"History", "INBOX", "Archive"},
		accountMailboxes: []deferredAccountMailbox{
			{accountID: 101, mailbox: "History"},
			{accountID: 202, mailbox: "inbox"},
			{accountID: 101, mailbox: "INBOX"},
			{accountID: 101, mailbox: "Archive"},
		},
	})
	if !reflect.DeepEqual(replay.mailboxes, []string{"INBOX", "Archive", "History"}) {
		t.Fatalf("global replay order=%v", replay.mailboxes)
	}
	// Global INBOX covers every account, so only non-Inbox account-qualified
	// requests remain after coalescing.
	if len(replay.accountMailboxes) != 0 {
		t.Fatalf("account replay order=%v, want global requests to cover every account", replay.accountMailboxes)
	}

	replay = runner.coalesceGenerationRecoveryReplay(generationRecoveryReplay{
		userID:    7,
		mailboxes: []string{"History"},
		accountMailboxes: []deferredAccountMailbox{
			{accountID: 101, mailbox: "Archive"},
			{accountID: 202, mailbox: "INBOX"},
			{accountID: 101, mailbox: "inbox"},
		},
	})
	if got := leadingGenerationRecoveryInboxes(replay.mailboxes); got != 0 {
		t.Fatalf("global leading Inbox count=%d", got)
	}
	if got := leadingGenerationRecoveryAccountInboxes(replay.accountMailboxes); got != 2 {
		t.Fatalf("account leading Inbox count=%d, want 2 before global History", got)
	}
}

func TestRunnerGlobalReleaseCollectsAccountQualifiedPendingRerun(t *testing.T) {
	r := NewRunner(nil)
	keys, ok := r.reserveMailboxes(7, []string{"INBOX"})
	if !ok {
		t.Fatalf("initial global mailbox reservation failed")
	}
	if !r.QueueAccountMailboxes(7, 101, []string{"inbox"}) {
		t.Fatalf("account mailbox rerun was not queued")
	}
	r.mu.Lock()
	delete(r.mailboxRunning, keys[0])
	accounts := r.takeAccountPendingForMailboxLocked(7, "INBOX")
	r.mu.Unlock()
	if !accounts[101] || len(accounts) != 1 {
		t.Fatalf("pending accounts = %#v", accounts)
	}
}

func TestRunnerMailboxMaintenanceBlocksSyncUntilFinished(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "maintenance@example.test", "Maintenance", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.UpsertMailAccount(ctx, store.MailAccount{
		UserID:              user.ID,
		Email:               "maintenance@example.test",
		Host:                "imap.example.test",
		Port:                993,
		Username:            "maintenance",
		EncryptedPassword:   "encrypted",
		UseTLS:              true,
		Mailbox:             "Archive",
		SyncIntervalMinutes: 15,
	})
	if err != nil {
		t.Fatal(err)
	}

	r := NewRunner(&Service{Store: db})
	senderStatsCalled := make(chan struct{}, 1)
	r.refreshSenderStatsForUser = func(context.Context, int64) error {
		senderStatsCalled <- struct{}{}
		return nil
	}
	started := make(chan struct{})
	release := make(chan struct{})
	run, ok, err := r.StartMailboxMaintenance(user.ID, store.Mailbox{ID: 55, AccountID: account.ID, Name: "Archive"}, "Purging", func(ctx context.Context, runID int64, progress *store.SyncProgress) error {
		close(started)
		select {
		case <-release:
			progress.MessagesTotal = 1
			progress.MessagesSeen = 1
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("StartMailboxMaintenance error = %v", err)
	}
	if !ok {
		t.Fatalf("maintenance did not start")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("maintenance task did not start")
	}
	if !r.IsAccountMailboxRunning(user.ID, account.ID, "archive") {
		t.Fatalf("maintenance did not reserve the account mailbox")
	}
	if r.StartAccountMailboxes(user.ID, account.ID, []string{"Archive"}) {
		t.Fatalf("account sync started while maintenance held the folder reservation")
	}
	if r.runMailboxes(user.ID, []string{"Archive"}) {
		t.Fatalf("global sync started while maintenance held the folder reservation")
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !r.IsAccountMailboxRunning(user.ID, account.ID, "archive") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("maintenance reservation was not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-senderStatsCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("sender stats did not refresh after maintenance released its mailbox reservation")
	}
	saved, err := db.GetSyncRunForUser(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "ok" || saved.MessagesSeen != 1 || saved.MessagesTotal != 1 || saved.MailboxesDone != 1 || saved.LatestNewFrom != "rolltop:maintenance" {
		t.Fatalf("maintenance run = %+v", saved)
	}
}

func TestGenerationRecoveryGateWaitsForActiveMailboxMaintenance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "recovery-maintenance@example.test", "Recovery Maintenance", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "encrypted", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunnerWithContext(ctx, &Service{Store: db})
	r.replayGenerationRecovery = func(generationRecoveryReplay) {}
	started := make(chan struct{})
	release := make(chan struct{})
	_, ok, err := r.StartMailboxMaintenance(user.ID, mailbox, "Maintenance", func(ctx context.Context, _ int64, _ *store.SyncProgress) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil || !ok {
		t.Fatalf("start maintenance ok=%t err=%v", ok, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not start")
	}
	r.SignalMailboxGenerationRecovery(user.ID)
	time.Sleep(20 * time.Millisecond)
	if !r.MailboxGenerationRecoveryActive(user.ID) {
		t.Fatal("recovery gate cleared while mailbox maintenance was active")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for r.MailboxGenerationRecoveryActive(user.ID) {
		if time.Now().After(deadline) {
			t.Fatal("recovery gate did not clear after mailbox maintenance released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunnerAttachmentIndexWaitsForForegroundOperation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "foreground-index@example.test", "Foreground Index", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	r := NewRunner(&Service{Store: db})
	finish, err := r.BeginForegroundOperation(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsRunning(user.ID) {
		t.Fatal("foreground operation was not reported as running")
	}
	if r.StartAttachmentIndex(user.ID) {
		t.Fatal("attachment index started during a foreground operation")
	}
	r.mu.Lock()
	pending := r.attachmentPending[user.ID]
	attachmentRunning := r.mailboxRunning[mailboxKey(user.ID, "__attachments__")]
	r.mu.Unlock()
	if !pending || attachmentRunning {
		t.Fatalf("attachment state pending=%t running=%t, want true/false", pending, attachmentRunning)
	}

	finish()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		pending = r.attachmentPending[user.ID]
		attachmentRunning = r.mailboxRunning[mailboxKey(user.ID, "__attachments__")]
		r.mu.Unlock()
		if !pending && !attachmentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attachment index did not resume and finish: pending=%t running=%t", pending, attachmentRunning)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.IsRunning(user.ID) {
		t.Fatal("foreground operation remained active after release")
	}
}

func TestRunnerForegroundWaitsForCanceledAttachmentWorker(t *testing.T) {
	r := NewRunner(nil)
	userID := int64(77)
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	canceled := make(chan struct{})
	allowExit := make(chan struct{})
	key := mailboxKey(userID, "__attachments__")
	r.mu.Lock()
	r.mailboxRunning[key] = true
	r.attachmentCancels[userID] = cancel
	r.attachmentDone[userID] = done
	r.mu.Unlock()
	go func() {
		<-workerCtx.Done()
		close(canceled)
		<-allowExit
		r.mu.Lock()
		delete(r.mailboxRunning, key)
		delete(r.attachmentCancels, userID)
		delete(r.attachmentDone, userID)
		r.mu.Unlock()
		close(done)
	}()

	type result struct {
		finish func()
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		finish, err := r.BeginForegroundOperation(context.Background(), userID)
		resultCh <- result{finish: finish, err: err}
	}()
	<-canceled
	select {
	case <-resultCh:
		t.Fatal("foreground operation returned before the canceled attachment worker exited")
	default:
	}
	close(allowExit)
	started := <-resultCh
	if started.err != nil {
		t.Fatal(started.err)
	}
	started.finish()
}

func TestRunnerForegroundWaitsForActiveGenerationRecoveryReplay(t *testing.T) {
	r := NewRunnerWithContext(context.Background(), nil)
	userID := int64(78)
	replayKey := accountMailboxKey(userID, 301, "INBOX")
	r.mu.Lock()
	r.generationRecoveryReplay[userID] = true
	r.mailboxRunning[replayKey] = true
	r.mu.Unlock()

	type result struct {
		finish func()
		err    error
	}
	started := make(chan result, 1)
	go func() {
		finish, err := r.BeginForegroundOperation(context.Background(), userID)
		started <- result{finish: finish, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case result := <-started:
		t.Fatalf("foreground operation overlapped replay reservation: %v", result.err)
	default:
	}
	r.mu.Lock()
	delete(r.mailboxRunning, replayKey)
	r.mu.Unlock()
	startedResult := <-started
	if startedResult.err != nil {
		t.Fatal(startedResult.err)
	}
	if _, ok := r.reserveGenerationRecoveryReplayAccountMailboxes(userID, 301, []string{"INBOX"}); ok {
		t.Fatal("replay resumed while the foreground operation remained active")
	}
	startedResult.finish()
	keys, ok := r.reserveGenerationRecoveryReplayAccountMailboxes(userID, 301, []string{"INBOX"})
	if !ok {
		t.Fatal("replay did not become ready after the foreground operation finished")
	}
	r.mu.Lock()
	for _, key := range keys {
		delete(r.mailboxRunning, key)
	}
	delete(r.generationRecoveryReplay, userID)
	r.mu.Unlock()
}

func TestRunnerForegroundWaitsForActiveGenerationRecoveryRun(t *testing.T) {
	r := NewRunnerWithContext(context.Background(), nil)
	userID := int64(79)
	r.mu.Lock()
	r.generationRecoveryRuns[userID] = true
	r.mu.Unlock()

	type result struct {
		finish func()
		err    error
	}
	started := make(chan result, 1)
	go func() {
		finish, err := r.BeginForegroundOperation(context.Background(), userID)
		started <- result{finish: finish, err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case result := <-started:
		t.Fatalf("foreground operation overlapped generation recovery: %v", result.err)
	default:
	}
	r.mu.Lock()
	delete(r.generationRecoveryRuns, userID)
	r.mu.Unlock()
	startedResult := <-started
	if startedResult.err != nil {
		t.Fatal(startedResult.err)
	}
	startedResult.finish()
	r.mu.Lock()
	foreground := r.foregroundRunning[userID]
	r.mu.Unlock()
	if foreground != 0 {
		t.Fatalf("foreground reservation count after finish=%d, want 0", foreground)
	}
}

func TestRunnerCanceledForegroundWaitReleasesReservation(t *testing.T) {
	r := NewRunnerWithContext(context.Background(), nil)
	userID := int64(80)
	r.mu.Lock()
	r.generationRecoveryRuns[userID] = true
	r.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := r.BeginForegroundOperation(ctx, userID)
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled foreground wait error=%v, want context canceled", err)
	}
	r.mu.Lock()
	foreground := r.foregroundRunning[userID]
	delete(r.generationRecoveryRuns, userID)
	r.mu.Unlock()
	if foreground != 0 {
		t.Fatalf("canceled foreground reservation count=%d, want 0", foreground)
	}
}

func TestGenerationRecoveryDerivedMaintenanceRunsOutsideReplayGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "replay-maintenance@example.test", "Replay Maintenance", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunnerWithContext(ctx, &Service{Store: db})
	maintenanceErr := errors.New("permanent derived maintenance failure")
	senderCalled := make(chan bool, 1)
	attachmentCalled := make(chan bool, 1)
	r.refreshSenderStatsForUser = func(context.Context, int64) error {
		senderCalled <- r.MailboxGenerationRecoveryActive(user.ID)
		return maintenanceErr
	}
	r.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
		attachmentCalled <- r.MailboxGenerationRecoveryActive(user.ID)
		return 0, maintenanceErr
	}
	r.mu.Lock()
	r.generationRecoveryReplay[user.ID] = true
	r.senderStatsPending[user.ID] = true
	r.attachmentPending[user.ID] = true
	r.mu.Unlock()
	go r.runGenerationRecoveryReplay(generationRecoveryReplay{userID: user.ID})

	for name, called := range map[string]<-chan bool{"sender stats": senderCalled, "search index": attachmentCalled} {
		select {
		case active := <-called:
			if active {
				t.Fatalf("%s ran while the recovery replay gate was active", name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s was not scheduled after recovery replay", name)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	var senderPending, attachmentPending bool
	for {
		r.mu.Lock()
		senderPending = r.senderStatsPending[user.ID]
		attachmentPending = r.attachmentPending[user.ID]
		attachmentDone := r.attachmentDone[user.ID]
		r.mu.Unlock()
		if senderPending && attachmentPending && attachmentDone == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("derived maintenance worker did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if r.MailboxGenerationRecoveryActive(user.ID) || !senderPending || !attachmentPending {
		t.Fatalf("maintenance handoff active=%t sender_pending=%t attachment_pending=%t",
			r.MailboxGenerationRecoveryActive(user.ID), senderPending, attachmentPending)
	}
}

func TestSenderStatsRefreshPreservesNewGenerationRecoveryRequest(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewRunner(&Service{Store: db})
	const userID = int64(81)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	r.refreshSenderStatsForUser = func(context.Context, int64) error {
		close(started)
		<-release
		return nil
	}
	go func() {
		r.RefreshSenderStats(userID)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("sender stats refresh did not start")
	}
	r.SignalMailboxGenerationRecovery(userID)
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sender stats refresh did not finish")
	}
	r.mu.Lock()
	pending := r.senderStatsPending[userID]
	r.mu.Unlock()
	if !pending {
		t.Fatal("older sender stats refresh erased a newer generation recovery request")
	}
}

func TestSenderStatsRefreshWaitsForOrdinaryMailboxWriter(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewRunner(&Service{Store: db})
	const userID = int64(82)
	called := make(chan struct{}, 1)
	r.refreshSenderStatsForUser = func(context.Context, int64) error {
		called <- struct{}{}
		return nil
	}

	r.mu.Lock()
	r.mailboxRunning[accountMailboxKey(userID, 101, "INBOX")] = true
	r.mu.Unlock()
	r.RefreshSenderStats(userID)
	select {
	case <-called:
		t.Fatal("sender stats overlapped an active mailbox writer")
	default:
	}
	r.mu.Lock()
	pending := r.senderStatsPending[userID]
	delete(r.mailboxRunning, accountMailboxKey(userID, 101, "INBOX"))
	r.mu.Unlock()
	if !pending {
		t.Fatal("sender stats work was not preserved while mailbox writer was active")
	}
	r.RefreshSenderStats(userID)
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("sender stats did not run after mailbox writer released")
	}
}

func TestMailboxReservationWaitsForActiveSenderStats(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewRunner(&Service{Store: db})
	const userID = int64(83)
	statsStarted := make(chan struct{})
	releaseStats := make(chan struct{})
	statsDone := make(chan struct{})
	r.refreshSenderStatsForUser = func(context.Context, int64) error {
		close(statsStarted)
		<-releaseStats
		return nil
	}
	go func() {
		r.RefreshSenderStats(userID)
		close(statsDone)
	}()
	select {
	case <-statsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("sender stats did not start")
	}

	type reservation struct {
		keys []string
		ok   bool
	}
	reserved := make(chan reservation, 1)
	go func() {
		keys, ok := r.reserveAccountMailboxes(userID, 101, []string{"INBOX"})
		reserved <- reservation{keys: keys, ok: ok}
	}()
	select {
	case <-reserved:
		t.Fatal("mailbox reservation overlapped active sender stats")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseStats)
	select {
	case <-statsDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sender stats did not finish")
	}
	var result reservation
	select {
	case result = <-reserved:
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox reservation did not resume after sender stats")
	}
	if !result.ok {
		t.Fatal("mailbox reservation was declined after sender stats released")
	}
	r.releaseAccountMailboxReservations(userID, 101, []string{"INBOX"}, result.keys)
}

func TestForegroundOperationSerializesExistingAndNewMailboxWriters(t *testing.T) {
	r := NewRunner(nil)
	const userID = int64(84)
	activeKeys, ok := r.reserveAccountMailboxes(userID, 101, []string{"INBOX"})
	if !ok {
		t.Fatal("initial mailbox reservation failed")
	}

	type foregroundResult struct {
		finish func()
		err    error
	}
	foregroundStarted := make(chan foregroundResult, 1)
	go func() {
		finish, err := r.BeginForegroundOperation(context.Background(), userID)
		foregroundStarted <- foregroundResult{finish: finish, err: err}
	}()
	select {
	case <-foregroundStarted:
		t.Fatal("foreground operation overlapped an existing mailbox writer")
	case <-time.After(30 * time.Millisecond):
	}
	r.releaseAccountMailboxReservations(userID, 101, []string{"INBOX"}, activeKeys)

	var foreground foregroundResult
	select {
	case foreground = <-foregroundStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground operation did not start after mailbox writer released")
	}
	if foreground.err != nil {
		t.Fatal(foreground.err)
	}

	type reservation struct {
		keys []string
		ok   bool
	}
	newMailbox := make(chan reservation, 1)
	go func() {
		keys, ok := r.reserveAccountMailboxes(userID, 202, []string{"INBOX"})
		newMailbox <- reservation{keys: keys, ok: ok}
	}()
	select {
	case <-newMailbox:
		t.Fatal("new mailbox writer overlapped the foreground operation")
	case <-time.After(30 * time.Millisecond):
	}
	foreground.finish()

	var mailbox reservation
	select {
	case mailbox = <-newMailbox:
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox writer did not resume after foreground operation")
	}
	if !mailbox.ok {
		t.Fatal("mailbox writer was declined after foreground operation released")
	}
	r.releaseAccountMailboxReservations(userID, 202, []string{"INBOX"}, mailbox.keys)
}

func TestForegroundOperationsAreSerializedPerUser(t *testing.T) {
	r := NewRunner(nil)
	const userID = int64(85)
	firstFinish, err := r.BeginForegroundOperation(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	secondStarted := make(chan func(), 1)
	go func() {
		finish, beginErr := r.BeginForegroundOperation(context.Background(), userID)
		if beginErr != nil {
			secondStarted <- nil
			return
		}
		secondStarted <- finish
	}()
	select {
	case <-secondStarted:
		t.Fatal("second foreground operation overlapped the first")
	case <-time.After(30 * time.Millisecond):
	}
	firstFinish()
	select {
	case secondFinish := <-secondStarted:
		if secondFinish == nil {
			t.Fatal("second foreground operation failed")
		}
		secondFinish()
	case <-time.After(2 * time.Second):
		t.Fatal("second foreground operation did not start after the first released")
	}
}

func TestForegroundOperationPreemptsNextAutoMailbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "auto-priority@example.test", "Auto Priority", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "INBOX,Archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"INBOX", "Archive"} {
		mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.UpdateMailboxSyncMode(ctx, user.ID, mailbox.ID, "auto"); err != nil {
			t.Fatal(err)
		}
	}
	fetcher := &autoForegroundPriorityFetcher{
		moveTestFetcher: &moveTestFetcher{},
		firstStarted:    make(chan struct{}, 1),
		releaseFirst:    make(chan struct{}),
		secondStarted:   make(chan struct{}, 1),
	}
	r := NewRunnerWithContext(ctx, &Service{Store: db, Fetcher: fetcher})
	if !r.Start(user.ID) {
		t.Fatal("account-wide sync did not start")
	}
	select {
	case <-fetcher.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first auto mailbox did not start")
	}

	type foregroundResult struct {
		finish func()
		err    error
	}
	foregroundStarted := make(chan foregroundResult, 1)
	go func() {
		finish, err := r.BeginForegroundOperation(ctx, user.ID)
		foregroundStarted <- foregroundResult{finish: finish, err: err}
	}()
	select {
	case result := <-foregroundStarted:
		t.Fatalf("foreground operation overlapped first auto mailbox: %v", result.err)
	case <-time.After(30 * time.Millisecond):
	}
	close(fetcher.releaseFirst)

	var foreground foregroundResult
	select {
	case foreground = <-foregroundStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground operation did not acquire after first auto mailbox")
	}
	if foreground.err != nil {
		t.Fatal(foreground.err)
	}
	select {
	case <-fetcher.secondStarted:
		t.Fatal("auto sync reserved its second mailbox before foreground work")
	case <-time.After(50 * time.Millisecond):
	}
	foreground.finish()
	select {
	case <-fetcher.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second auto mailbox did not resume after foreground work")
	}
	waitForRunnerUserIdle(t, r, user.ID)
}

func TestAutoMailboxPlanningWaitsForCanceledAttachmentWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "auto-plan-wait@example.test", "Auto Plan Wait", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "secret", UseTLS: true, Mailbox: "Archive",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunnerWithContext(ctx, &Service{Store: db, Fetcher: &moveTestFetcher{}})
	attachmentCanceled, releaseAttachment := installBlockedAttachmentWorker(r, user.ID)
	if !r.Start(user.ID) {
		t.Fatal("account-wide sync did not reserve")
	}
	select {
	case <-attachmentCanceled:
	case <-time.After(time.Second):
		t.Fatal("auto planning did not cancel attachment worker")
	}
	if _, err := db.GetMailbox(ctx, user.ID, account.ID, "Archive"); !store.IsNotFound(err) {
		t.Fatalf("auto planning touched mailbox before attachment exit: %v", err)
	}

	foregroundStarted := make(chan func(), 1)
	go func() {
		finish, err := r.BeginForegroundOperation(ctx, user.ID)
		if err != nil {
			foregroundStarted <- nil
			return
		}
		foregroundStarted <- finish
	}()
	releaseAttachment()
	var finish func()
	select {
	case finish = <-foregroundStarted:
		if finish == nil {
			t.Fatal("foreground operation failed while waiting for auto planning")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground operation did not start after auto planning")
	}
	if _, err := db.GetMailbox(ctx, user.ID, account.ID, "Archive"); err != nil {
		t.Fatalf("foreground operation overlapped unfinished auto planning: %v", err)
	}
	finish()
	waitForRunnerUserIdle(t, r, user.ID)
}

func TestForegroundQueueDefersWithoutWaitingOnItsOwnReservation(t *testing.T) {
	r := NewRunner(nil)
	const userID = int64(86)
	finish, err := r.BeginForegroundOperation(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan bool, 1)
	go func() {
		queued <- r.QueueAccountMailboxes(userID, 101, []string{"INBOX"})
	}()
	select {
	case ok := <-queued:
		if !ok {
			t.Fatal("foreground mailbox refresh was not queued")
		}
	case <-time.After(time.Second):
		t.Fatal("foreground mailbox refresh waited on its own reservation")
	}

	r.mu.Lock()
	deferred := len(r.foregroundDeferredAccts[userID])
	// This unit test has no sync Service. Remove the captured replay before
	// release so the assertion stays focused on the nonblocking queue path.
	delete(r.foregroundDeferredAccts, userID)
	r.mu.Unlock()
	if deferred != 1 {
		t.Fatalf("deferred foreground mailbox refreshes = %d, want 1", deferred)
	}
	finish()
}

func TestForegroundReleaseDoesNotInventDerivedMaintenance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "foreground-derived@example.test", "Foreground Derived", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunnerWithContext(ctx, &Service{Store: db})
	var senderCalls, attachmentCalls atomic.Int32
	r.refreshSenderStatsForUser = func(context.Context, int64) error {
		senderCalls.Add(1)
		return nil
	}
	r.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
		attachmentCalls.Add(1)
		return 0, nil
	}

	finish, err := r.BeginForegroundOperation(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	time.Sleep(50 * time.Millisecond)
	if senderCalls.Load() != 0 || attachmentCalls.Load() != 0 {
		t.Fatalf("foreground release invented derived work: sender=%d attachment=%d",
			senderCalls.Load(), attachmentCalls.Load())
	}
}

func TestOrdinaryMailboxSyncWaitsForCanceledAttachmentWorker(t *testing.T) {
	for _, test := range []struct {
		name  string
		start func(*Runner, int64, int64) bool
	}{
		{
			name: "global",
			start: func(r *Runner, userID, _ int64) bool {
				return r.StartMailboxes(userID, []string{"INBOX"})
			},
		},
		{
			name: "account",
			start: func(r *Runner, userID, accountID int64) bool {
				return r.StartAccountMailboxes(userID, accountID, []string{"INBOX"})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			user, account, _ := createRunnerMailboxFixture(t, ctx, db, test.name+"-wait@example.test")
			r := NewRunnerWithContext(ctx, &Service{Store: db, Fetcher: &moveTestFetcher{}})
			attachmentCanceled, releaseAttachment := installBlockedAttachmentWorker(r, user.ID)
			if !test.start(r, user.ID, account.ID) {
				t.Fatal("ordinary mailbox sync did not reserve")
			}
			select {
			case <-attachmentCanceled:
			case <-time.After(time.Second):
				t.Fatal("ordinary mailbox sync did not cancel attachment worker")
			}
			if runs, err := db.ListSyncRunsForUser(ctx, user.ID, 20); err != nil || len(runs) != 0 {
				t.Fatalf("sync entered Service before attachment exit: runs=%d err=%v", len(runs), err)
			}
			releaseAttachment()
			deadline := time.Now().Add(2 * time.Second)
			for {
				runs, err := db.ListSyncRunsForUser(ctx, user.ID, 20)
				if err != nil {
					t.Fatal(err)
				}
				if len(runs) > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("ordinary mailbox sync did not enter Service after attachment exit")
				}
				time.Sleep(5 * time.Millisecond)
			}
			waitForRunnerUserIdle(t, r, user.ID)
		})
	}
}

func TestMailboxMaintenanceWaitsForCanceledAttachmentWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, _, mailbox := createRunnerMailboxFixture(t, ctx, db, "maintenance-wait@example.test")
	r := NewRunnerWithContext(ctx, &Service{Store: db})
	attachmentCanceled, releaseAttachment := installBlockedAttachmentWorker(r, user.ID)
	type maintenanceResult struct {
		run store.SyncRun
		ok  bool
		err error
	}
	maintenanceCalled := make(chan struct{}, 1)
	result := make(chan maintenanceResult, 1)
	go func() {
		run, ok, err := r.StartMailboxMaintenance(user.ID, mailbox, "test", func(context.Context, int64, *store.SyncProgress) error {
			maintenanceCalled <- struct{}{}
			return nil
		})
		result <- maintenanceResult{run: run, ok: ok, err: err}
	}()
	select {
	case <-attachmentCanceled:
	case <-time.After(time.Second):
		t.Fatal("mailbox maintenance did not cancel attachment worker")
	}
	select {
	case got := <-result:
		t.Fatalf("mailbox maintenance returned before attachment exit: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-maintenanceCalled:
		t.Fatal("mailbox maintenance function ran before attachment exit")
	default:
	}
	if runs, err := db.ListSyncRunsForUser(ctx, user.ID, 20); err != nil || len(runs) != 0 {
		t.Fatalf("maintenance created a sync run before attachment exit: runs=%d err=%v", len(runs), err)
	}
	releaseAttachment()
	select {
	case got := <-result:
		if got.err != nil || !got.ok || got.run.ID == 0 {
			t.Fatalf("mailbox maintenance result after attachment exit: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox maintenance did not start after attachment exit")
	}
	select {
	case <-maintenanceCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox maintenance function did not run")
	}
	waitForRunnerUserIdle(t, r, user.ID)
}

func TestMailboxMaintenanceCancellationReleasesReservationWhileWaitingForAttachment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, _, mailbox := createRunnerMailboxFixture(t, ctx, db, "maintenance-cancel@example.test")
	r := NewRunnerWithContext(ctx, &Service{Store: db})
	attachmentCanceled, releaseAttachment := installBlockedAttachmentWorker(r, user.ID)
	defer releaseAttachment()
	result := make(chan error, 1)
	go func() {
		_, _, err := r.StartMailboxMaintenance(user.ID, mailbox, "test", func(context.Context, int64, *store.SyncProgress) error {
			return nil
		})
		result <- err
	}()
	select {
	case <-attachmentCanceled:
	case <-time.After(time.Second):
		t.Fatal("mailbox maintenance did not cancel attachment worker")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("maintenance cancellation error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mailbox maintenance did not return on cancellation")
	}
	if r.IsAccountMailboxRunning(user.ID, mailbox.AccountID, mailbox.Name) {
		t.Fatal("maintenance reservation remained after canceled attachment wait")
	}
}

func createRunnerMailboxFixture(t *testing.T, ctx context.Context, db *store.Store, email string) (store.User, store.MailAccount, store.Mailbox) {
	t.Helper()
	user, err := db.CreateUser(ctx, email, "Runner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := db.CreateMailAccount(ctx, store.MailAccount{
		UserID: user.ID, Email: user.Email, Host: "imap.example.test", Port: 993,
		Username: user.Email, EncryptedPassword: "secret", UseTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mailbox, err := db.GetOrCreateMailbox(ctx, user.ID, account.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	return user, account, mailbox
}

func installBlockedAttachmentWorker(r *Runner, userID int64) (<-chan struct{}, func()) {
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	key := mailboxKey(userID, "__attachments__")
	r.mu.Lock()
	r.mailboxRunning[key] = true
	r.attachmentCancels[userID] = cancel
	r.attachmentDone[userID] = done
	r.mu.Unlock()
	go func() {
		<-workerCtx.Done()
		close(canceled)
		<-release
		r.mu.Lock()
		delete(r.mailboxRunning, key)
		delete(r.attachmentCancels, userID)
		delete(r.attachmentDone, userID)
		r.mu.Unlock()
		close(done)
	}()
	return canceled, func() { close(release) }
}

func waitForRunnerUserIdle(t *testing.T, r *Runner, userID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.IsRunning(userID) {
		if time.Now().After(deadline) {
			t.Fatal("runner user work did not become idle")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForRunnerMaintenanceIdle(t, r, userID)
}

func TestRunnerAttachmentIndexWaitsForMailboxReservation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "mailbox-index@example.test", "Mailbox Index", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	r := NewRunner(&Service{Store: db})
	keys, ok := r.reserveAccountMailboxes(user.ID, 55, []string{"INBOX"})
	if !ok {
		t.Fatal("mailbox reservation failed")
	}
	if r.StartAttachmentIndex(user.ID) {
		t.Fatal("attachment index started during a mailbox reservation")
	}
	r.releaseAccountMailboxReservations(user.ID, 55, []string{"INBOX"}, keys)
	if !r.StartAttachmentIndex(user.ID) {
		t.Fatal("attachment index did not start after mailbox release")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		running := r.mailboxRunning[mailboxKey(user.ID, "__attachments__")]
		r.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attachment index did not finish after mailbox release")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunnerSerializesAttachmentAndSenderStatsHandoffs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "maintenance-handoff@example.test", "Maintenance Handoff", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("sender waits for attachment and healthy drain resumes", func(t *testing.T) {
		r := NewRunnerWithContext(ctx, &Service{Store: db})
		firstIndexStarted := make(chan struct{}, 1)
		secondIndexStarted := make(chan struct{}, 1)
		releaseFirstIndex := make(chan struct{})
		senderStarted := make(chan struct{}, 1)
		releaseSender := make(chan struct{})
		indexCalls := 0
		r.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
			indexCalls++
			if indexCalls == 1 {
				firstIndexStarted <- struct{}{}
				<-releaseFirstIndex
				return attachmentIndexBatchSize, nil
			}
			secondIndexStarted <- struct{}{}
			return 0, nil
		}
		r.refreshSenderStatsForUser = func(context.Context, int64) error {
			senderStarted <- struct{}{}
			<-releaseSender
			return nil
		}

		if !r.StartAttachmentIndex(user.ID) {
			t.Fatal("initial attachment index did not start")
		}
		awaitRunnerSignal(t, firstIndexStarted, "initial attachment index")
		r.RefreshSenderStats(user.ID)
		assertNoRunnerSignal(t, senderStarted, "sender stats overlapped attachment index")

		close(releaseFirstIndex)
		awaitRunnerSignal(t, senderStarted, "sender stats handoff")
		assertNoRunnerSignal(t, secondIndexStarted, "attachment drain overlapped sender stats")
		close(releaseSender)
		awaitRunnerSignal(t, secondIndexStarted, "resumed attachment drain")
		waitForRunnerMaintenanceIdle(t, r, user.ID)
	})

	t.Run("attachment waits for sender", func(t *testing.T) {
		r := NewRunnerWithContext(ctx, &Service{Store: db})
		senderStarted := make(chan struct{}, 1)
		releaseSender := make(chan struct{})
		senderDone := make(chan struct{})
		indexStarted := make(chan struct{}, 1)
		r.refreshSenderStatsForUser = func(context.Context, int64) error {
			senderStarted <- struct{}{}
			<-releaseSender
			return nil
		}
		r.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
			indexStarted <- struct{}{}
			return 0, nil
		}

		go func() {
			r.RefreshSenderStats(user.ID)
			close(senderDone)
		}()
		awaitRunnerSignal(t, senderStarted, "sender stats")
		if r.StartAttachmentIndex(user.ID) {
			t.Fatal("attachment index started while sender stats was running")
		}
		assertNoRunnerSignal(t, indexStarted, "attachment index overlapped sender stats")
		close(releaseSender)
		awaitRunnerSignal(t, indexStarted, "attachment handoff")
		awaitRunnerSignal(t, senderDone, "sender stats completion")
		waitForRunnerMaintenanceIdle(t, r, user.ID)
	})

	t.Run("index failure does not retry through sender handoff", func(t *testing.T) {
		r := NewRunnerWithContext(ctx, &Service{Store: db})
		indexStarted := make(chan struct{}, 2)
		releaseIndex := make(chan struct{})
		senderStarted := make(chan struct{}, 1)
		indexErr := errors.New("global search index failure")
		r.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
			indexStarted <- struct{}{}
			<-releaseIndex
			return 0, indexErr
		}
		r.refreshSenderStatsForUser = func(context.Context, int64) error {
			senderStarted <- struct{}{}
			return nil
		}

		if !r.StartAttachmentIndex(user.ID) {
			t.Fatal("attachment index did not start")
		}
		awaitRunnerSignal(t, indexStarted, "failing attachment index")
		r.RefreshSenderStats(user.ID)
		close(releaseIndex)
		awaitRunnerSignal(t, senderStarted, "sender stats after index failure")
		assertNoRunnerSignal(t, indexStarted, "automatic attachment retry after index failure")
		waitForRunnerMaintenanceIdle(t, r, user.ID)

		r.mu.Lock()
		pending := r.attachmentPending[user.ID]
		resumeAfterStats := r.attachmentResumeAfterStats[user.ID]
		r.mu.Unlock()
		if !pending || resumeAfterStats {
			t.Fatalf("failed index pending=%t resume_after_stats=%t, want true, false", pending, resumeAfterStats)
		}
	})
}

func awaitRunnerSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func assertNoRunnerSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
		t.Fatal(name)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitForRunnerMaintenanceIdle(t *testing.T, r *Runner, userID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		busy := r.attachmentDone[userID] != nil || r.senderStatsRunning[userID]
		r.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("runner maintenance did not become idle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
