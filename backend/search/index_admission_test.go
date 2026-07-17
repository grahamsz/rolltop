package search

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"

	"rolltop/backend/store"
)

type observedNewBatchIndex struct {
	delegatedBleveIndex
	called chan struct{}
	once   sync.Once
}

type preparedAdmissionResult struct {
	chunk preparedMessageIndexChunk
	lease *bleveWriteLease
	err   error
}

func (i *observedNewBatchIndex) NewBatch() *bleve.Batch {
	i.once.Do(func() { close(i.called) })
	return i.delegatedBleveIndex.NewBatch()
}

func TestIndexMessagesAcquiresCoordinatorBeforeConstructingBleveBatch(t *testing.T) {
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	observed := &observedNewBatchIndex{delegatedBleveIndex: svc.index, called: make(chan struct{})}
	svc.index = observed
	svc.writeCoordinator = newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 1, MaxActiveBytes: 1024 * 1024, DiagnosticAfter: time.Hour,
	})
	active, err := svc.writeCoordinator.Acquire(context.Background(), coordinatorRequest(99, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer active.Release()

	result := make(chan error, 1)
	go func() {
		result <- svc.IndexMessage(context.Background(), store.MessageRecord{
			ID: 1, UserID: 1, AccountID: 2, MailboxID: 3, Subject: "admitted first",
		}, nil)
	}()
	waitForCoordinatorQueue(t, svc.writeCoordinator, 1)
	select {
	case <-observed.called:
		t.Fatal("Bleve batch was constructed before coordinator admission")
	case <-time.After(20 * time.Millisecond):
	}

	active.Release()
	select {
	case <-observed.called:
	case <-time.After(time.Second):
		t.Fatal("Bleve batch was not constructed after coordinator admission")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("IndexMessage did not finish after coordinator admission")
	}
}

func TestMessageProjectionWaitsForCoordinatorReservation(t *testing.T) {
	svc := newSearchService()
	svc.writeCoordinator = newBleveWriteCoordinator(bleveWriteCoordinatorConfig{
		MaxActive: 1, MaxActiveBytes: 32 * 1024 * 1024, DiagnosticAfter: time.Hour,
	})
	active, err := svc.writeCoordinator.Acquire(context.Background(), coordinatorRequest(99, bleveWriteNormal, 1))
	if err != nil {
		t.Fatal(err)
	}

	plans, err := planMessageIndexTenants(context.Background(), []MessageIndexDocument{{Message: store.MessageRecord{
		ID: 1, UserID: 7, Subject: "project only after admission",
	}}})
	if err != nil {
		active.Release()
		t.Fatal(err)
	}
	projected := make(chan struct{}, 1)
	iterator := newMessageIndexChunkIterator(plans[0], messageIndexChunkTargetBytes,
		func(message store.MessageRecord, _ []AttachmentDoc) map[string]any {
			projected <- struct{}{}
			return map[string]any{"subject": message.Subject}
		})
	result := make(chan preparedAdmissionResult, 1)
	go func() {
		chunk, lease, err := svc.prepareNextMessageIndexChunk(context.Background(), iterator)
		result <- preparedAdmissionResult{chunk: chunk, lease: lease, err: err}
	}()
	waitForCoordinatorQueue(t, svc.writeCoordinator, 1)
	projectedBeforeAdmission := false
	select {
	case <-projected:
		projectedBeforeAdmission = true
	case <-time.After(20 * time.Millisecond):
	}

	active.Release()
	var admitted preparedAdmissionResult
	select {
	case admitted = <-result:
	case <-time.After(time.Second):
		t.Fatal("message projection did not proceed after coordinator admission")
	}
	if admitted.lease != nil {
		svc.finishUnstartedWriterOperation(admitted.lease)
	}
	if admitted.err != nil {
		t.Fatal(admitted.err)
	}
	if projectedBeforeAdmission {
		t.Fatal("message projection occurred before coordinator admission")
	}
	select {
	case <-projected:
	default:
		t.Fatal("message was not projected after admission")
	}
	if got := preparedMessageIDs(admitted.chunk); len(got) != 1 || got[0] != 1 {
		t.Fatalf("admitted chunk IDs = %v", got)
	}
	svc.writeCoordinator.mu.Lock()
	activeWrites := svc.writeCoordinator.activeWrites
	activeBytes := svc.writeCoordinator.activeBytes
	svc.writeCoordinator.mu.Unlock()
	if activeWrites != 0 || activeBytes != 0 {
		t.Fatalf("coordinator remained active: writes=%d bytes=%d", activeWrites, activeBytes)
	}
}
