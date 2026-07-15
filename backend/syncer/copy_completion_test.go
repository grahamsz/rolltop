// File overview: Background-copy completion callback guarantees.

package syncer

import (
	"context"
	"testing"
	"time"
)

func TestStartCopyMessagesCallsCompletionAfterFailure(t *testing.T) {
	fixture := newMoveTestFixture(t)
	done := make(chan struct{})

	if _, err := fixture.service.StartCopyMessages(context.Background(), fixture.userID,
		[]int64{fixture.message.ID}, fixture.destination.ID, func() { close(done) }); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("failed background copy did not invoke its completion callback")
	}
}
