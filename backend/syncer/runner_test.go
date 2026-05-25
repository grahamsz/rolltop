// File overview: Tests for sync runner mailbox reservation semantics.

package syncer

import "testing"

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
