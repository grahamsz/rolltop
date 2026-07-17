package search

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBleveWriteCoordinatorPrioritizesForegroundAndAgesBackground(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 1, MaxActiveBytes: 100, AgingInterval: 20 * time.Millisecond, DiagnosticAfter: time.Hour,
	})
	active, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}
	background := acquireCoordinatorAsync(coordinator, coordinatorRequest(2, bleveWriteBackground, 1))
	waitForCoordinatorQueue(t, coordinator, 1)
	foreground := acquireCoordinatorAsync(coordinator, coordinatorRequest(3, bleveWriteForeground, 1))
	waitForCoordinatorQueue(t, coordinator, 2)
	active.Release()
	foregroundLease := awaitCoordinatorLease(t, foreground, "foreground")
	assertNoCoordinatorLease(t, background, "background overtook foreground")
	foregroundLease.Release()
	awaitCoordinatorLease(t, background, "background").Release()

	active, err = coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}
	aged := acquireCoordinatorAsync(coordinator, coordinatorRequest(2, bleveWriteBackground, 1))
	waitForCoordinatorQueue(t, coordinator, 1)
	time.Sleep(45 * time.Millisecond)
	newForeground := acquireCoordinatorAsync(coordinator, coordinatorRequest(3, bleveWriteForeground, 1))
	waitForCoordinatorQueue(t, coordinator, 2)
	active.Release()
	agedLease := awaitCoordinatorLease(t, aged, "aged background")
	assertNoCoordinatorLease(t, newForeground, "new foreground overtook aged background")
	agedLease.Release()
	awaitCoordinatorLease(t, newForeground, "new foreground").Release()
}

func TestBleveWriteCoordinatorEnforcesTenantAndByteBudgets(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 2, MaxActiveBytes: 10, DiagnosticAfter: time.Hour,
	})
	first, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 7))
	if err != nil {
		t.Fatal(err)
	}
	sameTenant := acquireCoordinatorAsync(coordinator, coordinatorRequest(1, bleveWriteForeground, 1))
	tooLarge := acquireCoordinatorAsync(coordinator, coordinatorRequest(2, bleveWriteNormal, 5))
	waitForCoordinatorQueue(t, coordinator, 2)
	fitting := acquireCoordinatorAsync(coordinator, coordinatorRequest(3, bleveWriteBackground, 3))
	waitForCoordinatorQueue(t, coordinator, 3)
	assertNoCoordinatorLease(t, sameTenant, "same tenant ran concurrently")
	assertNoCoordinatorLease(t, tooLarge, "byte budget was exceeded")
	assertNoCoordinatorLease(t, fitting, "lower-priority fitting job bypassed byte-blocked work")
	first.Release()
	sameLease := awaitCoordinatorLease(t, sameTenant, "same tenant after release")
	tooLargeLease := awaitCoordinatorLease(t, tooLarge, "byte-limited tenant after release")
	sameLease.Release()
	tooLargeLease.Release()
	awaitCoordinatorLease(t, fitting, "fitting tenant after older jobs").Release()

	oversize, err := coordinator.Acquire(context.Background(), coordinatorRequest(9, bleveWriteNormal, 20))
	if err != nil {
		t.Fatal(err)
	}
	oversize.Release()
}

func TestBleveWriteLeaseReconcilesProjectedAndActualBytes(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 2, MaxActiveBytes: 10, DiagnosticAfter: time.Hour,
	})
	lease, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 6))
	if err != nil {
		t.Fatal(err)
	}
	lease.UpdateBytes(9)
	coordinator.mu.Lock()
	if coordinator.activeBytes != 9 {
		coordinator.mu.Unlock()
		t.Fatalf("active bytes after reconciliation = %d, want 9", coordinator.activeBytes)
	}
	coordinator.mu.Unlock()

	waiting := acquireCoordinatorAsync(coordinator, coordinatorRequest(2, bleveWriteNormal, 2))
	waitForCoordinatorQueue(t, coordinator, 1)
	assertNoCoordinatorLease(t, waiting, "actual byte size was not enforced")
	lease.UpdateBytes(8)
	second := awaitCoordinatorLease(t, waiting, "write after actual byte reduction")
	second.Release()
	lease.Release()

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.activeBytes != 0 || coordinator.activeWrites != 0 {
		t.Fatalf("coordinator remained active after reconciled leases: bytes=%d writes=%d", coordinator.activeBytes, coordinator.activeWrites)
	}
}

func TestBleveWriteCoordinatorDrainsForAgedByteHeavyWork(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 2, MaxActiveBytes: 10, AgingInterval: 10 * time.Millisecond, DiagnosticAfter: time.Hour,
	})
	first, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 6))
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Acquire(context.Background(), coordinatorRequest(2, bleveWriteNormal, 4))
	if err != nil {
		t.Fatal(err)
	}
	heavy := acquireCoordinatorAsync(coordinator, coordinatorRequest(3, bleveWriteBackground, 8))
	waitForCoordinatorQueue(t, coordinator, 1)
	time.Sleep(25 * time.Millisecond)
	second.Release()
	laterSmall := acquireCoordinatorAsync(coordinator, coordinatorRequest(4, bleveWriteForeground, 4))
	waitForCoordinatorQueue(t, coordinator, 2)
	assertNoCoordinatorLease(t, laterSmall, "later small write prevented byte-budget drain")
	first.Release()
	heavyLease := awaitCoordinatorLease(t, heavy, "aged byte-heavy write")
	assertNoCoordinatorLease(t, laterSmall, "later small write overtook aged byte-heavy write")
	heavyLease.Release()
	awaitCoordinatorLease(t, laterSmall, "later small write after drain").Release()
}

func TestBleveWriteCoordinatorDispatchesAfterCancelingByteBarrier(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 2, MaxActiveBytes: 10, DiagnosticAfter: time.Hour,
	})
	active, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 6))
	if err != nil {
		t.Fatal(err)
	}
	heavyContext, cancelHeavy := context.WithCancel(context.Background())
	heavy := acquireCoordinatorAsyncContext(coordinator, heavyContext, coordinatorRequest(2, bleveWriteNormal, 8))
	waitForCoordinatorQueue(t, coordinator, 1)
	small := acquireCoordinatorAsync(coordinator, coordinatorRequest(3, bleveWriteNormal, 4))
	waitForCoordinatorQueue(t, coordinator, 2)
	assertNoCoordinatorLease(t, small, "small write bypassed byte barrier")

	cancelHeavy()
	heavyResult := <-heavy
	if heavyResult.err != context.Canceled || heavyResult.lease != nil {
		t.Fatalf("canceled barrier acquisition = %+v", heavyResult)
	}
	smallLease := awaitCoordinatorLease(t, small, "small write after barrier cancellation")
	smallLease.Release()
	active.Release()
}

func TestBleveWriteCoordinatorCancellationAndOneShotDiagnostic(t *testing.T) {
	diagnostics := make(chan bleveWriteWaitDiagnostic, 2)
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 1, MaxActiveBytes: 100, DiagnosticAfter: 10 * time.Millisecond,
		OnWait: func(diagnostic bleveWriteWaitDiagnostic) { diagnostics <- diagnostic },
	})
	active, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 8))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := acquireCoordinatorAsyncContext(coordinator, ctx, coordinatorRequest(1, bleveWriteBackground, 4))
	waitForCoordinatorQueue(t, coordinator, 1)
	select {
	case diagnostic := <-diagnostics:
		if diagnostic.QueueDepth != 1 || diagnostic.ActiveWrites != 1 || diagnostic.ActiveBytes != 8 || !diagnostic.SameUserActive {
			t.Fatalf("wait diagnostic = %+v", diagnostic)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not emit wait diagnostic")
	}
	select {
	case diagnostic := <-diagnostics:
		t.Fatalf("coordinator emitted repeated diagnostic: %+v", diagnostic)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	result := <-waiting
	if result.err != context.Canceled || result.lease != nil {
		t.Fatalf("canceled acquisition = %+v", result)
	}
	active.Release()
	waitForCoordinatorQueue(t, coordinator, 0)
}

func TestBleveWriteCoordinatorWaitDiagnosticCannotBlockCancellation(t *testing.T) {
	diagnosticStarted := make(chan struct{})
	releaseDiagnostic := make(chan struct{})
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 1, MaxActiveBytes: 100, DiagnosticAfter: time.Millisecond,
		OnWait: func(bleveWriteWaitDiagnostic) {
			close(diagnosticStarted)
			<-releaseDiagnostic
		},
	})
	active, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := acquireCoordinatorAsyncContext(coordinator, ctx, coordinatorRequest(2, bleveWriteNormal, 1))
	select {
	case <-diagnosticStarted:
	case <-time.After(time.Second):
		t.Fatal("wait diagnostic did not start")
	}
	cancel()
	select {
	case result := <-waiting:
		if result.err != context.Canceled || result.lease != nil {
			t.Fatalf("canceled acquisition = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking wait diagnostic prevented cancellation")
	}
	close(releaseDiagnostic)
	active.Release()
}

func TestBleveWriteCoordinatorRecoversWaitDiagnosticPanic(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 1, MaxActiveBytes: 100, DiagnosticAfter: time.Millisecond,
		OnWait: func(bleveWriteWaitDiagnostic) {
			panic("diagnostic failure")
		},
	})
	active, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}
	waiting := acquireCoordinatorAsync(coordinator, coordinatorRequest(2, bleveWriteNormal, 1))
	time.Sleep(10 * time.Millisecond)
	active.Release()
	awaitCoordinatorLease(t, waiting, "write after diagnostic panic").Release()
}

type coordinatorAcquireResult struct {
	lease *bleveWriteLease
	err   error
}

func coordinatorRequest(userID int64, priority bleveWritePriority, bytes uint64) bleveWriteRequest {
	return bleveWriteRequest{
		Details:  bleveErrorContext{Operation: "index-batch", UserID: userID, Documents: 1},
		Priority: priority, Bytes: bytes,
	}
}

func acquireCoordinatorAsync(coordinator *bleveWriteCoordinator, request bleveWriteRequest) <-chan coordinatorAcquireResult {
	return acquireCoordinatorAsyncContext(coordinator, context.Background(), request)
}

func acquireCoordinatorAsyncContext(coordinator *bleveWriteCoordinator, ctx context.Context, request bleveWriteRequest) <-chan coordinatorAcquireResult {
	result := make(chan coordinatorAcquireResult, 1)
	go func() {
		lease, err := coordinator.Acquire(ctx, request)
		result <- coordinatorAcquireResult{lease: lease, err: err}
	}()
	return result
}

func awaitCoordinatorLease(t *testing.T, result <-chan coordinatorAcquireResult, label string) *bleveWriteLease {
	t.Helper()
	select {
	case acquired := <-result:
		if acquired.err != nil || acquired.lease == nil {
			t.Fatalf("%s acquisition = %+v", label, acquired)
		}
		return acquired.lease
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func assertNoCoordinatorLease(t *testing.T, result <-chan coordinatorAcquireResult, label string) {
	t.Helper()
	select {
	case acquired := <-result:
		if acquired.lease != nil {
			acquired.lease.Release()
		}
		t.Fatalf("%s: %+v", label, acquired)
	case <-time.After(20 * time.Millisecond):
	}
}

func waitForCoordinatorQueue(t *testing.T, coordinator *bleveWriteCoordinator, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		got := len(coordinator.waiters)
		coordinator.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("coordinator queue depth=%d, want %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBleveWriteLeaseReleaseIsIdempotent(t *testing.T) {
	coordinator := newBleveWriteCoordinator(bleveWriteCoordinatorConfig{MaxActive: 1, DiagnosticAfter: time.Hour})
	lease, err := coordinator.Acquire(context.Background(), coordinatorRequest(1, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 10 {
		group.Add(1)
		go func() {
			defer group.Done()
			lease.Release()
		}()
	}
	group.Wait()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.activeWrites != 0 || coordinator.activeBytes != 0 || len(coordinator.activeUsers) != 0 {
		t.Fatalf("coordinator remained active after idempotent release")
	}
}

func TestBlevePriorityForOperationHonorsExplicitContextAndForegroundMutations(t *testing.T) {
	if got := blevePriorityForOperation(context.Background(), "index-batch"); got != bleveWriteNormal {
		t.Fatalf("default index priority = %s, want normal", got)
	}
	if got := blevePriorityForOperation(context.Background(), "purge-mailbox-batch"); got != bleveWriteForeground {
		t.Fatalf("purge priority = %s, want foreground", got)
	}
	if got := blevePriorityForOperation(WithBackgroundIndexing(context.Background()), "delete-batch"); got != bleveWriteBackground {
		t.Fatalf("explicit background delete priority = %s, want background", got)
	}
	if got := blevePriorityForOperation(WithForegroundIndexing(context.Background()), "index-batch"); got != bleveWriteForeground {
		t.Fatalf("explicit foreground index priority = %s, want foreground", got)
	}
}
