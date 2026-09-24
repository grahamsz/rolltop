// File overview: Unit tests for per-folder sync failure backoff.

package syncer

import (
	"testing"
	"time"
)

func shrinkMailboxBackoffForTest(t *testing.T, base, max time.Duration) {
	t.Helper()
	oldBase, oldMax := mailboxBackoffBaseDelay, mailboxBackoffMaxDelay
	mailboxBackoffBaseDelay, mailboxBackoffMaxDelay = base, max
	t.Cleanup(func() {
		mailboxBackoffBaseDelay, mailboxBackoffMaxDelay = oldBase, oldMax
	})
}

func TestMailboxBackoffDelayDoublesToCap(t *testing.T) {
	shrinkMailboxBackoffForTest(t, 15*time.Minute, 4*time.Hour)
	cases := map[int]time.Duration{
		0: 15 * time.Minute,
		1: 15 * time.Minute,
		2: 30 * time.Minute,
		3: 60 * time.Minute,
		4: 120 * time.Minute,
		5: 240 * time.Minute,
		9: 240 * time.Minute,
	}
	for failures, want := range cases {
		if got := mailboxBackoffDelay(failures); got != want {
			t.Errorf("mailboxBackoffDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestMailboxBackoffRecordDelayAndClear(t *testing.T) {
	shrinkMailboxBackoffForTest(t, 50*time.Millisecond, 100*time.Millisecond)
	s := &Service{}
	const userID, accountID = int64(7), int64(9)

	if s.mailboxSyncDelayed(userID, accountID, "INBOX") {
		t.Fatal("fresh folder must not be delayed")
	}
	s.recordMailboxSyncFailure(userID, accountID, "INBOX")
	if !s.mailboxSyncDelayed(userID, accountID, "INBOX") {
		t.Fatal("folder must be delayed right after a failure")
	}
	// A different folder and a different tenant are unaffected.
	if s.mailboxSyncDelayed(userID, accountID, "Archive/2025") {
		t.Fatal("healthy folder must not inherit another folder's backoff")
	}
	if s.mailboxSyncDelayed(userID+1, accountID, "INBOX") {
		t.Fatal("backoff must be scoped to the tenant")
	}
	// A different account for the same tenant is unaffected.
	if s.mailboxSyncDelayed(userID, accountID+1, "INBOX") {
		t.Fatal("backoff must be scoped to the account")
	}
	// Mailbox names match case-insensitively, like the rest of the syncer.
	if !s.mailboxSyncDelayed(userID, accountID, "inbox") {
		t.Fatal("backoff lookup must be case-insensitive")
	}
	time.Sleep(120 * time.Millisecond)
	if s.mailboxSyncDelayed(userID, accountID, "INBOX") {
		t.Fatal("backoff must expire after its delay")
	}
	s.recordMailboxSyncFailure(userID, accountID, "INBOX")
	s.recordMailboxSyncFailure(userID, accountID, "INBOX")
	if !s.mailboxSyncDelayed(userID, accountID, "INBOX") {
		t.Fatal("consecutive failures must extend the backoff")
	}
	s.recordMailboxSyncSuccess(userID, accountID, "INBOX")
	if s.mailboxSyncDelayed(userID, accountID, "INBOX") {
		t.Fatal("a successful sync must clear the backoff")
	}
	// Success on a folder with no backoff entry is a no-op.
	s.recordMailboxSyncSuccess(userID, accountID, "Never-Failed")
}
