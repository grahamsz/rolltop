package syncer

import (
	"context"
	"testing"
)

func TestRunnerNotifiesAfterMailboxReservationIsReleased(t *testing.T) {
	for _, accountOnly := range []bool{false, true} {
		name := "all accounts"
		if accountOnly {
			name = "one account"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newMoveTestFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := NewRunnerWithContext(ctx, fixture.service)
			otherUserID := fixture.userID + 100
			if _, ok := runner.reserveMailboxes(otherUserID, []string{"INBOX"}); !ok {
				t.Fatal("could not reserve other user's mailbox")
			}
			var observed []bool
			notify := func(userID int64) {
				if userID != fixture.userID {
					t.Errorf("notification leaked to user %d", userID)
				}
				// Models an SSE subscriber loading chrome immediately when notified.
				// It also verifies callbacks run outside the scheduler mutex.
				observed = append(observed, runner.IsRunning(userID))
			}
			fixture.service.Notify = notify
			fixture.service.NotifyProgress = notify
			mailboxes := []string{"INBOX"}
			var err error
			if accountOnly {
				keys, ok := runner.reserveAccountMailboxes(fixture.userID, fixture.account.ID, mailboxes)
				if !ok {
					t.Fatal("could not reserve mailbox")
				}
				err = runner.runReservedAccountMailboxes(fixture.userID, fixture.account.ID, mailboxes, keys)
			} else {
				keys, ok := runner.reserveMailboxes(fixture.userID, mailboxes)
				if !ok {
					t.Fatal("could not reserve mailbox")
				}
				err = runner.runReservedMailboxes(fixture.userID, mailboxes, keys)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(observed) == 0 || observed[len(observed)-1] {
				t.Fatalf("last chrome notification still showed sync running: %v", observed)
			}
			if !runner.IsRunning(otherUserID) {
				t.Fatal("completion released another user's work")
			}
		})
	}
}

func TestRunnerNotifiesAfterForegroundOperationIsReleased(t *testing.T) {
	service := &Service{}
	runner := NewRunner(service)
	var observed []bool
	service.NotifyProgress = func(userID int64) { observed = append(observed, runner.IsRunning(userID)) }
	finish, err := runner.BeginForegroundOperation(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	service.notifyProgress(7)
	finish()
	if len(observed) < 2 || observed[len(observed)-1] {
		t.Fatalf("foreground completion left subscribers busy: %v", observed)
	}
}
