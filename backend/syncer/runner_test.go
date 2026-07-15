// File overview: Tests for sync runner mailbox reservation semantics.

package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"rolltop/backend/store"
)

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

func TestGenerationRecoveryMaintenanceRetainsPendingBitsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(context.Background(), "replay-maintenance@example.test", "Replay Maintenance", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunnerWithContext(ctx, &Service{Store: db})
	r.mu.Lock()
	r.generationRecoveryReplay[user.ID] = true
	r.senderStatsPending[user.ID] = true
	claimedSender := r.claimGenerationRecoverySenderStatsLocked(user.ID)
	r.attachmentPending[user.ID] = true
	attachmentCtx, attachmentDone, claimedAttachments := r.claimGenerationRecoveryAttachmentBatchLocked(user.ID)
	r.mu.Unlock()
	if !claimedSender || !claimedAttachments {
		t.Fatalf("maintenance claims sender=%t attachments=%t", claimedSender, claimedAttachments)
	}
	cancel()
	if err := r.refreshSenderStatsDuringGenerationRecoveryReplay(user.ID); err == nil {
		t.Fatal("sender stats unexpectedly succeeded with a canceled replay context")
	}
	if err := r.indexAttachmentsDuringGenerationRecoveryReplay(user.ID, attachmentCtx, attachmentDone); err == nil {
		t.Fatal("attachment batch unexpectedly succeeded with a canceled replay context")
	}
	r.mu.Lock()
	senderPending := r.senderStatsPending[user.ID]
	attachmentPending := r.attachmentPending[user.ID]
	senderRunning := r.mailboxRunning[mailboxKey(user.ID, "__recovery_sender_stats__")]
	attachmentRunning := r.mailboxRunning[mailboxKey(user.ID, "__attachments__")]
	r.mu.Unlock()
	if !senderPending || !attachmentPending || senderRunning || attachmentRunning {
		t.Fatalf("canceled maintenance sender_pending=%t attachment_pending=%t sender_running=%t attachment_running=%t",
			senderPending, attachmentPending, senderRunning, attachmentRunning)
	}
}

func TestGenerationRecoveryPermanentMaintenanceErrorsFailOpen(t *testing.T) {
	for _, phase := range []string{"sender", "attachments"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			user, err := db.CreateUser(ctx, "replay-fail-open-"+phase+"@example.test", "Fail Open", "hash", false)
			if err != nil {
				t.Fatal(err)
			}
			r := NewRunnerWithContext(ctx, &Service{Store: db})
			permanentErr := errors.New("permanent derived maintenance failure")
			var calls atomic.Int64
			if phase == "sender" {
				r.refreshSenderStatsForUser = func(context.Context, int64) error {
					calls.Add(1)
					return permanentErr
				}
			} else {
				r.indexAttachmentsForUser = func(context.Context, int64, int) (int, error) {
					calls.Add(1)
					return 0, permanentErr
				}
			}
			r.mu.Lock()
			r.generationRecoveryReplay[user.ID] = true
			if phase == "sender" {
				r.senderStatsPending[user.ID] = true
			} else {
				r.attachmentPending[user.ID] = true
			}
			r.mu.Unlock()
			go r.runGenerationRecoveryReplay(generationRecoveryReplay{userID: user.ID})

			deadline := time.Now().Add(2 * time.Second)
			for {
				r.mu.Lock()
				pending := r.senderStatsPending[user.ID]
				if phase == "attachments" {
					pending = r.attachmentPending[user.ID]
				}
				workerDone := r.attachmentDone[user.ID] == nil
				callCount := calls.Load()
				r.mu.Unlock()
				if !r.MailboxGenerationRecoveryActive(user.ID) && pending && workerDone && callCount >= 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("permanent %s failure did not fail open: active=%t pending=%t worker_done=%t calls=%d",
						phase, r.MailboxGenerationRecoveryActive(user.ID), pending, workerDone, callCount)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
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
