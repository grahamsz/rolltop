// File overview: Tests for sync runner mailbox reservation semantics.

package syncer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mailmirror/backend/store"
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
	rerun := r.releaseAccountMailboxReservations(7, []string{"Gmail Forward"}, keys)
	if len(rerun) != 1 || rerun[0] != "Gmail Forward" {
		t.Fatalf("rerun = %#v", rerun)
	}
	if r.mailboxPending[mailboxKey(7, "Gmail Forward")] {
		t.Fatalf("pending mailbox key was not cleared")
	}
	if r.IsAccountMailboxRunning(7, 101, "Gmail Forward") {
		t.Fatalf("account mailbox reservation was not released")
	}
}

func TestRunnerMailboxMaintenanceBlocksSyncUntilFinished(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mailmirror.db"))
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
	if saved.Status != "ok" || saved.MessagesSeen != 1 || saved.MessagesTotal != 1 || saved.MailboxesDone != 1 || saved.LatestNewFrom != "mailmirror:maintenance" {
		t.Fatalf("maintenance run = %+v", saved)
	}
}
