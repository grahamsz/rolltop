package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessageTransferReopenProofIsAttemptScopedAndAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := createPendingMoveTestUser(t, ctx, db, "dispatch-reopen@example.test")
	account := createPendingMoveTestAccount(t, ctx, db, user, "primary")
	source := arrivalTestMailbox(t, ctx, db, user, account, "Source", 901)
	destination := arrivalTestMailbox(t, ctx, db, user, account, "Destination", 902)
	raw := []byte("Message-ID: <dispatch-reopen@example.test>\r\n\r\nbody\r\n")
	message, _ := arrivalTestMessage(t, ctx, db, user, account, source, 1, raw,
		"<dispatch-reopen@example.test>", "thread:dispatch-reopen", time.Now().UTC())
	transfer, err := db.StageMessageTransfer(ctx, user.ID, message.ID, destination.ID, "move", "")
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := db.ClaimMessageTransferDispatchForOwner(ctx, user.ID, transfer.ID, "same-process")
	if err != nil || !claimed {
		t.Fatalf("claim claimed=%v err=%v", claimed, err)
	}
	if err := db.FinishMessageTransferDispatch(ctx, user.ID, transfer.ID, claim); err != nil {
		t.Fatal(err)
	}

	var reopened atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, reopenErr := db.ReopenMessageTransferDispatchAfterProof(ctx, user.ID, transfer.ID, claim, "same-process")
			if reopenErr != nil {
				t.Errorf("reopen: %v", reopenErr)
				return
			}
			if ok {
				reopened.Add(1)
			}
		}()
	}
	wg.Wait()
	if reopened.Load() != 1 {
		t.Fatalf("successful concurrent reopens=%d, want 1", reopened.Load())
	}
	newClaim, claimed, err := db.ClaimMessageTransferDispatchForOwner(ctx, user.ID, transfer.ID, "same-process")
	if err != nil || !claimed || newClaim.Attempt != claim.Attempt+1 {
		t.Fatalf("new claim=%+v claimed=%v err=%v", newClaim, claimed, err)
	}
	if ok, err := db.ReopenMessageTransferDispatchAfterProof(ctx, user.ID, transfer.ID, claim, "other-process"); err != nil || ok {
		t.Fatalf("stale proof reopened newer claim ok=%v err=%v", ok, err)
	}
}
