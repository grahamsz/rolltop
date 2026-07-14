// File overview: Tests for sync runner mailbox reservation semantics.

package syncer

import (
	"context"
	"path/filepath"
	"reflect"
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
