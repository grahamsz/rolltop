package search

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rolltop/backend/store"
)

func TestConcurrentFirstReadSharesExistingTenantIndex(t *testing.T) {
	root := t.TempDir()
	// Production restarts reopen an existing index. Duplicate opens of this
	// file wait on Bleve's exclusive Bolt lock; duplicate creates fail instead.
	index, err := openIndex(filepath.Join(root, "7", "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	service, err := OpenPerUser(root)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var waiting sync.WaitGroup
	waiting.Add(workers)
	start := make(chan struct{})
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			waiting.Done()
			<-start
			_, err := service.CountMailboxMessages(context.Background(), 7, 11)
			results <- err
		}()
	}
	waiting.Wait()
	completed := 0
	t.Cleanup(func() {
		// Release a duplicate opener even on the broken implementation.
		if err := service.Close(); err != nil {
			t.Error(err)
		}
		timeout := time.After(5 * time.Second)
		for completed < workers {
			select {
			case <-results:
				completed++
			case <-timeout:
				t.Error("index open workers did not exit after service close")
				return
			}
		}
	})
	close(start)
	timeout := time.After(2 * time.Second)
	for completed < workers {
		select {
		case err := <-results:
			completed++
			if err != nil {
				t.Fatal(err)
			}
		case <-timeout:
			t.Fatalf("only %d/%d first reads completed; concurrent index open is stuck", completed, workers)
		}
	}
}

func TestIndexOpenCancellationDoesNotBlockAnotherTenant(t *testing.T) {
	root := t.TempDir()
	held, err := openIndex(filepath.Join(root, "7", "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	closeHeld := sync.OnceFunc(func() {
		if err := held.Close(); err != nil {
			t.Error(err)
		}
	})
	defer closeHeld()
	service, err := OpenPerUser(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := service.CountMailboxMessages(ctx, 7, 11); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked-index read = %v, want caller deadline", err)
	}
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	if err := service.IndexMessage(otherCtx, store.MessageRecord{ID: 1, UserID: 8, MailboxID: 11, Subject: "other tenant", Date: time.Now()}, nil); err != nil {
		t.Fatalf("another tenant was blocked by the index open: %v", err)
	}
	closeHeld()
	if n, err := service.CountMailboxMessages(otherCtx, 7, 11); err != nil || n != 0 {
		t.Fatalf("resumed tenant read = %d, %v; want no other tenant's documents", n, err)
	}
	if n, err := service.CountMailboxMessages(otherCtx, 8, 11); err != nil || n != 1 {
		t.Fatalf("other tenant count = %d, %v", n, err)
	}
}

func TestCloseWaitsForPendingIndexOpenAndReleasesItsHandle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "7", "bleve")
	held, err := openIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	closeHeld := sync.OnceFunc(func() {
		if err := held.Close(); err != nil {
			t.Error(err)
		}
	})
	defer closeHeld()
	service, err := OpenPerUser(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := service.CountMailboxMessages(ctx, 7, 11); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked-index read = %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		service.mu.Lock()
		closing := service.closing
		service.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close did not start")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closed:
		t.Fatalf("close returned before the pending open finished: %v", err)
	default:
	}
	closeHeld()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish after the file lock was released")
	}
	// A late open must have been closed, not cached or leaked after shutdown.
	reopened, err := openIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if _, err := service.indexForUser(context.Background(), 7); !errors.Is(err, errSearchServiceClosing) {
		t.Fatalf("closed service reopened an index: %v", err)
	}
}
