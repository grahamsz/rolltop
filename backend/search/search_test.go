// File overview: Tests for search indexing, query behavior, tenant isolation, and highlighting.

package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"

	"rolltop/backend/store"
)

type observedErrContext struct {
	context.Context
	errCalled chan struct{}
}

type delegatedBleveIndex interface {
	bleve.Index
}

type blockingBatchIndex struct {
	delegatedBleveIndex
	started     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	finishOnce  sync.Once
	err         error
}

func (i *blockingBatchIndex) Batch(*bleve.Batch) error {
	i.startOnce.Do(func() { close(i.started) })
	<-i.release
	i.finishOnce.Do(func() { close(i.finished) })
	return i.err
}

type failingBleveIndex struct {
	delegatedBleveIndex
	batchErr  error
	searchErr error
}

func (i *failingBleveIndex) Batch(*bleve.Batch) error {
	return i.batchErr
}

func (i *failingBleveIndex) SearchInContext(context.Context, *bleve.SearchRequest) (*bleve.SearchResult, error) {
	return nil, i.searchErr
}

type capturedBleveLogs struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturedBleveLogs) Printf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *capturedBleveLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (i *blockingBatchIndex) unblock() {
	i.releaseOnce.Do(func() { close(i.release) })
}

func (c *observedErrContext) Err() error {
	select {
	case c.errCalled <- struct{}{}:
	default:
	}
	return c.Context.Err()
}

func TestWriterOperationsHonorCancellationWhileWaiting(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *Service) error
	}{
		{
			name: "index messages",
			run: func(ctx context.Context, svc *Service) error {
				return svc.IndexMessage(ctx, store.MessageRecord{
					ID: 2, UserID: 1, MailboxID: 10, Subject: "new message", Date: time.Now(),
				}, nil)
			},
		},
		{
			name: "delete messages",
			run: func(ctx context.Context, svc *Service) error {
				return svc.DeleteMessages(ctx, 1, []int64{1})
			},
		},
		{
			name: "purge mailbox",
			run: func(ctx context.Context, svc *Service) error {
				_, err := svc.PurgeMailbox(ctx, 1, 10)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			seed := store.MessageRecord{
				ID: 1, UserID: 1, MailboxID: 10, Subject: "seed message", Date: time.Now(),
			}
			if err := svc.IndexMessage(context.Background(), seed, nil); err != nil {
				t.Fatal(err)
			}

			writer := svc.writerForUser(1)
			writer.Lock()
			writerHeld := true
			defer func() {
				if writerHeld {
					writer.Unlock()
				}
			}()

			baseCtx, cancel := context.WithCancel(context.Background())
			ctx := &observedErrContext{Context: baseCtx, errCalled: make(chan struct{}, 1)}
			result := make(chan error, 1)
			go func() {
				result <- tt.run(ctx, svc)
			}()

			select {
			case <-ctx.errCalled:
			case <-time.After(time.Second):
				cancel()
				t.Fatal("operation did not reach the writer lock")
			}
			cancel()

			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("operation error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not stop after cancellation")
			}

			writer.Unlock()
			writerHeld = false
			if !writer.TryLock() {
				t.Fatal("writer lock remained held after cancellation")
			}
			writer.Unlock()

			if err := tt.run(context.Background(), svc); err != nil {
				t.Fatalf("operation after cancellation: %v", err)
			}
		})
	}
}

func TestWriterLockHandsOffToQueuedWaiterBeforeLaterTryLock(t *testing.T) {
	service := &Service{writerWaitLogAfter: time.Hour}
	writer := newWriterLock()
	writer.Lock()
	mainHeld := true

	acquired := make(chan error, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWaiter := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		if mainHeld {
			writer.Unlock()
		}
		releaseWaiter()
	}()
	go func() {
		err := service.lockWriter(context.Background(), writer, bleveErrorContext{Operation: "purge-mailbox-batch", UserID: 1, MailboxID: 10, Documents: 251})
		acquired <- err
		if err == nil {
			<-release
			writer.Unlock()
		}
	}()

	deadline := time.Now().Add(time.Second)
	for writer.queuedWaiters() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter did not enter writer admission queue")
		}
		time.Sleep(time.Millisecond)
	}
	writer.Unlock()
	mainHeld = false
	if writer.TryLock() {
		writer.Unlock()
		t.Fatal("later TryLock barged ahead of queued mailbox maintenance")
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued mailbox maintenance did not receive writer handoff")
	}
	releaseWaiter()
}

func TestWriterLockReportsOneBoundedWaitDiagnosticWithoutTimingOut(t *testing.T) {
	service := &Service{writerWaitLogAfter: 10 * time.Millisecond}
	logs := make(chan string, 2)
	service.bleveErrorLog = func(format string, args ...any) {
		logs <- fmt.Sprintf(format, args...)
	}
	writer := newWriterLock()
	writer.Lock()
	writer.setActive(bleveErrorContext{
		Operation: "index-batch", UserID: 1, AccountID: 2, MailboxID: 3, Documents: 25,
	})
	defer func() {
		writer.clearActive()
		writer.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- service.lockWriter(ctx, writer, bleveErrorContext{
			Operation: "purge-mailbox-batch", UserID: 1, AccountID: 2, MailboxID: 4, Documents: 251,
		})
	}()

	select {
	case line := <-logs:
		for _, want := range []string{
			`operation="purge-mailbox-batch"`, "user_id=1", "account_id=2", "mailbox_id=4", "documents=251",
			`active_operation="index-batch"`, "active_mailbox_id=3", "active_documents=25",
		} {
			if !strings.Contains(line, want) {
				t.Fatalf("writer wait diagnostic %q does not contain %q", line, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("writer wait diagnostic was not emitted")
	}
	select {
	case line := <-logs:
		t.Fatalf("writer wait emitted an unbounded repeated diagnostic: %q", line)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case err := <-result:
		t.Fatalf("diagnostic timed out the writer wait: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled writer wait error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled writer wait did not return")
	}
}

func TestBleveBatchCancellationDoesNotWaitForUnderlyingWrite(t *testing.T) {
	for _, test := range []struct {
		name      string
		prepare   func(*Service) error
		operation func(context.Context, *Service) error
	}{
		{
			name: "index",
			operation: func(ctx context.Context, svc *Service) error {
				return svc.IndexMessage(ctx, store.MessageRecord{
					ID: 1, UserID: 1, MailboxID: 10, Subject: "blocked commit", Date: time.Now(),
				}, nil)
			},
		},
		{
			name: "delete",
			prepare: func(svc *Service) error {
				return svc.IndexMessage(context.Background(), store.MessageRecord{
					ID: 1, UserID: 1, MailboxID: 10, Subject: "delete blocked commit", Date: time.Now(),
				}, nil)
			},
			operation: func(ctx context.Context, svc *Service) error {
				return svc.DeleteMessage(ctx, 1, 1)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertCanceledBleveBatch(t, test.prepare, test.operation)
		})
	}
}

func TestBleveFailuresAreLoggedWithoutSearchOrMessageContent(t *testing.T) {
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &capturedBleveLogs{}
	svc.bleveErrorLog = logs.Printf
	failing := &failingBleveIndex{
		delegatedBleveIndex: svc.index,
		batchErr:            errors.New("injected index storage failure"),
	}
	svc.index = failing
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.IndexMessage(context.Background(), store.MessageRecord{
		ID: 17, UserID: 41, MailboxID: 23,
		Subject:  "private subject must not be logged",
		BodyText: "private body must not be logged",
		Date:     time.Now(),
	}, nil); err == nil {
		t.Fatal("index failure was not returned")
	}
	failing.batchErr = nil
	failing.searchErr = errors.New("injected query storage failure")
	if _, err := svc.Search(context.Background(), 41, "private query must not be logged", 10, 0); err == nil {
		t.Fatal("search failure was not returned")
	}

	output := logs.String()
	for _, want := range []string{
		`bleve error operation="index-batch" user_id=41`,
		"documents=1",
		"injected index storage failure",
		`bleve error operation="search" user_id=41`,
		"injected query storage failure",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Bleve diagnostics %q do not contain %q", output, want)
		}
	}
	for _, private := range []string{"private subject", "private body", "private query"} {
		if strings.Contains(output, private) {
			t.Fatalf("Bleve diagnostics exposed private content %q: %q", private, output)
		}
	}
}

func TestBleveRequestCancellationDoesNotPolluteOperationalLogs(t *testing.T) {
	for _, canceledErr := range []error{context.Canceled, context.DeadlineExceeded} {
		svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
		if err != nil {
			t.Fatal(err)
		}
		logs := &capturedBleveLogs{}
		svc.bleveErrorLog = logs.Printf
		svc.index = &failingBleveIndex{delegatedBleveIndex: svc.index, searchErr: canceledErr}
		if _, err := svc.Search(context.Background(), 12, "canceled query", 10, 0); !errors.Is(err, canceledErr) {
			t.Fatalf("search error=%v, want %v", err, canceledErr)
		}
		if output := logs.String(); output != "" {
			t.Fatalf("routine cancellation produced Bleve diagnostics: %q", output)
		}
		_ = svc.Close()
	}
}

func TestDetachedBleveBatchFailureIsLoggedAfterCallerCancellation(t *testing.T) {
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &capturedBleveLogs{}
	svc.bleveErrorLog = logs.Printf
	blocking := &blockingBatchIndex{
		delegatedBleveIndex: svc.index,
		started:             make(chan struct{}),
		release:             make(chan struct{}),
		finished:            make(chan struct{}),
		err:                 errors.New("injected detached storage failure"),
	}
	svc.index = blocking
	t.Cleanup(func() {
		blocking.unblock()
		_ = svc.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- svc.IndexMessage(ctx, store.MessageRecord{
			ID: 1, UserID: 9, MailboxID: 4, Subject: "detached write", Date: time.Now(),
		}, nil)
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("Bleve batch did not start")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller error=%v, want context cancellation", err)
	}
	blocking.unblock()
	select {
	case <-blocking.finished:
	case <-time.After(time.Second):
		t.Fatal("detached Bleve batch did not finish")
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(logs.String(), "injected detached storage failure") {
		if time.Now().After(deadline) {
			t.Fatalf("detached Bleve failure was not logged: %q", logs.String())
		}
		time.Sleep(time.Millisecond)
	}
	if output := logs.String(); !strings.Contains(output, `operation="index-batch" user_id=9`) {
		t.Fatalf("detached Bleve diagnostics missing safe scope: %q", output)
	}
}

func TestLazyPerUserOpenLogsOriginalBleveCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "users")
	corruptPath := filepath.Join(root, "73", "bleve")
	if err := os.MkdirAll(corruptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := OpenPerUser(root)
	if err != nil {
		t.Fatal(err)
	}
	logs := &capturedBleveLogs{}
	svc.bleveErrorLog = logs.Printf
	t.Cleanup(func() { _ = svc.Close() })

	if _, err := svc.Search(context.Background(), 73, "anything", 10, 0); !errors.Is(err, bleve.ErrorIndexMetaMissing) {
		t.Fatalf("lazy corrupt-index error=%v, want original Bleve metadata error", err)
	}
	output := logs.String()
	if !strings.Contains(output, `operation="open-index" user_id=73`) || !strings.Contains(output, "metadata missing") {
		t.Fatalf("corrupt Bleve open was not logged with its original cause: %q", output)
	}
	if strings.Contains(output, "path already exists") {
		t.Fatalf("corrupt Bleve open was replaced by a create error: %q", output)
	}
}

func assertCanceledBleveBatch(t *testing.T, prepare func(*Service) error,
	operation func(context.Context, *Service) error,
) {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		if err := prepare(svc); err != nil {
			t.Fatal(err)
		}
	}
	blocking := &blockingBatchIndex{
		delegatedBleveIndex: svc.index,
		started:             make(chan struct{}),
		release:             make(chan struct{}),
		finished:            make(chan struct{}),
	}
	svc.index = blocking
	t.Cleanup(func() {
		blocking.unblock()
		_ = svc.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- operation(ctx, svc)
	}()

	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("Bleve batch did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write remained blocked after cancellation")
	}
	select {
	case <-blocking.finished:
		t.Fatal("underlying Bleve batch finished before it was released")
	default:
	}

	writer := svc.writerForUser(1)
	if writer.TryLock() {
		writer.Unlock()
		t.Fatal("writer lock was released while the Bleve batch was still running")
	}

	blocking.unblock()
	select {
	case <-blocking.finished:
	case <-time.After(time.Second):
		t.Fatal("underlying Bleve batch did not finish after release")
	}
	deadline := time.Now().Add(time.Second)
	for !writer.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("writer lock was not released after the Bleve batch finished")
		}
		time.Sleep(time.Millisecond)
	}
	writer.Unlock()
}

func TestCloseWaitsForDetachedBleveBatch(t *testing.T) {
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingBatchIndex{
		delegatedBleveIndex: svc.index,
		started:             make(chan struct{}),
		release:             make(chan struct{}),
		finished:            make(chan struct{}),
	}
	svc.index = blocking
	t.Cleanup(func() {
		blocking.unblock()
		_ = svc.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- svc.IndexMessage(ctx, store.MessageRecord{
			ID: 1, UserID: 1, MailboxID: 10, Subject: "close waits", Date: time.Now(),
		}, nil)
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("Bleve batch did not start")
	}
	cancel()
	if err := <-writeResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("write error=%v, want context canceled", err)
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- svc.Close() }()
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before detached batch finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	blocking.unblock()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after detached batch released")
	}
}

func TestCloseCanBeRetriedAfterDetachedWriterTimeout(t *testing.T) {
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	svc.closeWriterTimeout = 20 * time.Millisecond
	blocking := &blockingBatchIndex{
		delegatedBleveIndex: svc.index,
		started:             make(chan struct{}),
		release:             make(chan struct{}),
		finished:            make(chan struct{}),
	}
	svc.index = blocking
	t.Cleanup(func() {
		blocking.unblock()
		_ = svc.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	writeResult := make(chan error, 1)
	go func() {
		writeResult <- svc.IndexMessage(ctx, store.MessageRecord{
			ID: 1, UserID: 1, MailboxID: 10, Subject: "retry close", Date: time.Now(),
		}, nil)
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("Bleve batch did not start")
	}
	cancel()
	if err := <-writeResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("write error=%v, want context canceled", err)
	}
	if err := svc.Close(); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("first Close error = %v, want writer timeout", err)
	}

	blocking.unblock()
	if err := svc.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
}

func TestOpenPerUserKeepsDuplicateMessageIDsSeparate(t *testing.T) {
	ctx := context.Background()
	svc, err := OpenPerUser(filepath.Join(t.TempDir(), "users"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "alpha", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 2, Subject: "beta", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}

	ids, err := svc.Search(ctx, 1, "alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("user 1 ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 2, "alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("user 2 alpha ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 2, "beta", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("user 2 beta ids = %v", ids)
	}
}

func TestCountMailboxMessagesIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, MailboxID: 10, Subject: "one", Date: time.Now()},
		{ID: 2, UserID: 1, MailboxID: 10, Subject: "two", Date: time.Now()},
		{ID: 3, UserID: 1, MailboxID: 20, Subject: "three", Date: time.Now()},
		{ID: 4, UserID: 2, MailboxID: 10, Subject: "four", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	count, err := svc.CountMailboxMessages(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("user 1 mailbox 10 count = %d", count)
	}
	count, err = svc.CountMailboxMessages(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user 1 mailbox 20 count = %d", count)
	}
	count, err = svc.CountMailboxMessages(ctx, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user 2 mailbox 10 count = %d", count)
	}
}

func TestCountUserMessagesIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "one", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "two", Date: time.Now()},
		{ID: 3, UserID: 2, Subject: "three", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	count, err := svc.CountUserMessages(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("user 1 count = %d", count)
	}
	count, err = svc.CountUserMessages(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user 2 count = %d", count)
	}
}

func TestPurgeMailboxSearchIndexIsTenantAndMailboxScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, MailboxID: 10, Subject: "purge me", Date: time.Now()},
		{ID: 2, UserID: 1, MailboxID: 10, Subject: "purge me too", Date: time.Now()},
		{ID: 3, UserID: 1, MailboxID: 20, Subject: "keep same user", Date: time.Now()},
		{ID: 4, UserID: 2, MailboxID: 10, Subject: "keep other user", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.MailboxMessageIDs(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || !ids[1] || !ids[2] {
		t.Fatalf("mailbox ids before purge = %#v", ids)
	}
	deleted, err := svc.PurgeMailbox(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d", deleted)
	}
	for _, tt := range []struct {
		userID    int64
		mailboxID int64
		want      int
	}{
		{userID: 1, mailboxID: 10, want: 0},
		{userID: 1, mailboxID: 20, want: 1},
		{userID: 2, mailboxID: 10, want: 1},
	} {
		count, err := svc.CountMailboxMessages(ctx, tt.userID, tt.mailboxID)
		if err != nil {
			t.Fatal(err)
		}
		if count != tt.want {
			t.Fatalf("count user=%d mailbox=%d = %d, want %d", tt.userID, tt.mailboxID, count, tt.want)
		}
	}
}

func TestDeleteMessagesIsTenantScopedInCombinedIndex(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, msg := range []store.MessageRecord{
		{ID: 1, UserID: 1, MailboxID: 10, Subject: "delete owner", Date: time.Now()},
		{ID: 2, UserID: 2, MailboxID: 20, Subject: "keep other tenant", Date: time.Now()},
	} {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	var deletedBatches []int
	if err := svc.DeleteMessagesWithProgress(ctx, 1, []int64{1, 2}, func(deleted int) error {
		deletedBatches = append(deletedBatches, deleted)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(deletedBatches) != 1 || deletedBatches[0] != 1 {
		t.Fatalf("tenant-scoped delete progress=%v, want [1]", deletedBatches)
	}
	ownerIDs, err := svc.MessageIDsIndexed(ctx, 1, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if ownerIDs[1] {
		t.Fatal("owner document survived tenant-scoped batch delete")
	}
	otherIDs, err := svc.MessageIDsIndexed(ctx, 2, []int64{2})
	if err != nil {
		t.Fatal(err)
	}
	if !otherIDs[2] {
		t.Fatal("tenant-scoped batch delete removed another user's document")
	}
}

func TestSearchRecentStillAppliesTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "alpha older", BodyText: "needle", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "beta newest", BodyText: "not a match", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 1, Subject: "alpha newer", BodyText: "needle", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 4, UserID: 2, Subject: "alpha other tenant", BodyText: "needle", Date: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen[1] || !seen[3] {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchAppliesAttachmentOperator(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "older attachment", HasAttachments: true, Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, []AttachmentDoc{{Filename: "one.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 2, UserID: 1, Subject: "newer no attachment", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 3, UserID: 1, Subject: "newer attachment", HasAttachments: true, Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, []AttachmentDoc{{Filename: "three.txt"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "has:attachment", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen[1] || !seen[3] {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchStarredOperatorsAreTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "alpha starred", IsStarred: true, Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "alpha plain", IsStarred: false, Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 2, Subject: "alpha other tenant", IsStarred: true, Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "alpha is:starred", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("starred ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "alpha is:notstarred", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("not starred ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 2, "alpha is:starred", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("other tenant starred ids = %v", ids)
	}
}

func TestSearchLanguageOperatorIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "bonjour", BodyText: "facture", LanguageCode: "fr", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "hello", BodyText: "invoice", LanguageCode: "en", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 2, Subject: "bonjour other tenant", BodyText: "facture", LanguageCode: "fr", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "lang:fr", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("fr ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "lang:fr facture", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("combined ids = %v", ids)
	}
}

func TestSearchMatchesCompoundedSpacing(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "River Rise notice", BodyText: "water level update", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "riverrise", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}

	ids, err = svc.Search(ctx, 1, "riverrse", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("typo ids = %v", ids)
	}
}

func TestSearchPlainMultiWordRequiresAllTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "dark room setup", BodyText: "lighting notes", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "dark hallway", BodyText: "single-term match only", Date: time.Now()},
		{ID: 3, UserID: 1, Subject: "guest room", BodyText: "single-term match only", Date: time.Now()},
		{ID: 4, UserID: 1, Subject: "dark hallway", BodyText: "a separate room reference", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "dark room", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[1] || !got[4] {
		t.Fatalf("expected both-term matches, ids = %v", ids)
	}
	if got[2] || got[3] {
		t.Fatalf("single-term matches should not be returned, ids = %v", ids)
	}
}

func TestSearchHitsReportsActualMatchedTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "dark room setup", BodyText: "lighting notes", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "dark hallway", BodyText: "single-term match only", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := svc.SearchHitsWithOptions(ctx, 1, "darkk room", 10, 0, SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	terms := map[string]bool{}
	for _, term := range hits[0].Terms {
		terms[term] = true
	}
	if !terms["dark"] || !terms["room"] {
		t.Fatalf("terms = %v", hits[0].Terms)
	}
}

func TestExplainMessageWithOptionsReturnsScoreLocationsAndRawTree(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msg := store.MessageRecord{ID: 10, UserID: 1, Subject: "housing report", BodyText: "The committee discussed housing policy.", Date: time.Now()}
	if err := svc.IndexMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	result, ok, err := svc.ExplainMessageWithOptions(ctx, 1, 10, "housing", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected message to match")
	}
	if result.Score <= 0 {
		t.Fatalf("score = %f", result.Score)
	}
	if result.Raw == nil {
		t.Fatal("expected raw score explanation")
	}
	if len(result.FieldMatches) == 0 {
		t.Fatalf("field matches = %#v", result.FieldMatches)
	}
	if len(result.QueryTerms) == 0 {
		t.Fatalf("query terms = %#v", result.QueryTerms)
	}
	if len(result.TermContributions) == 0 {
		t.Fatalf("term contributions = %#v", result.TermContributions)
	}
	found := false
	for _, match := range result.FieldMatches {
		for _, term := range match.Terms {
			if term == "housing" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("field matches did not include housing: %#v", result.FieldMatches)
	}
	if _, ok, err := svc.ExplainMessageWithOptions(ctx, 2, 10, "housing", SearchOptions{}); err != nil || ok {
		t.Fatalf("cross-user explain ok=%v err=%v", ok, err)
	}
}

func TestExplainMessagesWithOptionsReturnsBestCandidate(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	messages := []store.MessageRecord{
		{ID: 20, UserID: 1, Subject: "checking in", BodyText: "No searched term here.", Date: time.Now()},
		{ID: 21, UserID: 1, Subject: "checking in", BodyText: "Nick mentioned the fund notice twice. Nick is available today.", Date: time.Now()},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	result, ok, err := svc.ExplainMessagesWithOptions(ctx, 1, []int64{20, 21}, "nick", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || result.ID != 21 {
		t.Fatalf("result ok=%v id=%d", ok, result.ID)
	}
	if len(result.QueryTerms) == 0 {
		t.Fatalf("query terms = %#v", result.QueryTerms)
	}
}

func TestSearchPrioritizesCompactPhraseOverFuzzyRecency(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "old useful match", BodyText: "No thanks, I have not got round to setting up a dark room yet.", Date: now.AddDate(-1, 0, 0)},
		{ID: 2, UserID: 1, Subject: "newer weak match", BodyText: "The storage room is ready for pickup.", Date: now.Add(-2 * time.Hour)},
		{ID: 3, UserID: 1, Subject: "newer close word", BodyText: "Wardroom availability changed this week.", Date: now.Add(-time.Hour)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "darkroom", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
	for _, id := range ids {
		if id == 2 || id == 3 {
			t.Fatalf("weak partial/fuzzy match returned for darkroom: ids = %v", ids)
		}
	}
}

func TestSearchDoesNotSplitShortCompactWordIntoFuzzyFragments(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "unrelated recent note", BodyText: "I'll be in front of BRM05 at 11:15, so we can get to the restaurant by 11:30.", Date: now},
		{ID: 2, UserID: 1, Subject: "Shipped: Ilford film", BodyText: "Your Ilford HP5 order is out for delivery.", Date: now.AddDate(0, 0, -30)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "ilford", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchDoesNotSplitCompactWordIntoThreeLetterFragments(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Housing update", BodyText: "Housing application status", Date: now},
		{ID: 2, UserID: 1, Subject: "Suffix fragment", BodyText: "ing", Date: now.Add(time.Hour)},
		{ID: 3, UserID: 1, Subject: "Split fragment", BodyText: "hous ing", Date: now.Add(2 * time.Hour)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "housing", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestQuotedCompactWordDoesNotSplitOrFuzzyMatch(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Darkroom supplies", BodyText: "Darkroom trays and chemicals", Date: now},
		{ID: 2, UserID: 1, Subject: "Dark room setup", BodyText: "A dark room with a safe light", Date: now},
		{ID: 3, UserID: 1, Subject: "Wardroom", BodyText: "Wardroom schedule", Date: now},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, `"darkroom"`, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchOptionsCanDisableFuzzyMatching(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "darkroom supplies", Date: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "darkrom", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("default fuzzy ids = %v", ids)
	}
	ids, err = svc.SearchWithOptions(ctx, 1, "darkrom", 10, 0, SearchOptions{Behavior: SearchBehavior{Fuzzy: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("fuzzy off ids = %v", ids)
	}
}

func TestSearchOptionsCanExcludeAttachmentText(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if err := svc.IndexMessage(ctx, store.MessageRecord{ID: 1, UserID: 1, Subject: "plain note", Date: time.Now(), HasAttachments: true}, []AttachmentDoc{{Filename: "report.pdf", ContentType: "application/pdf", Text: "peculiarterm"}}); err != nil {
		t.Fatal(err)
	}
	ids, err := svc.Search(ctx, 1, "peculiarterm", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("default attachment ids = %v", ids)
	}
	ids, err = svc.SearchWithOptions(ctx, 1, "peculiarterm", 10, 0, SearchOptions{Behavior: SearchBehavior{AttachmentWeight: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("attachment text off ids = %v", ids)
	}
}

func TestSearchMatchesFromDomain(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "route", FromAddr: "Support <help@mxroute.com>", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "other", FromAddr: "alerts@example.com", Date: time.Now()},
		{ID: 3, UserID: 2, Subject: "route other user", FromAddr: "help@mxroute.com", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	for _, query := range []string{"mxroute.com", "from:mxroute.com", "from:@mxroute.com"} {
		ids, err := svc.Search(ctx, 1, query, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != 1 {
			t.Fatalf("%q ids = %v", query, ids)
		}
	}
}

func TestSearchFromFullEmailRequiresLocalAndDomain(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "target", FromAddr: "Target <target.sender@example.com>", Date: time.Now()},
		{ID: 2, UserID: 1, Subject: "same domain", FromAddr: "Other <other@example.com>", Date: time.Now()},
		{ID: 3, UserID: 1, Subject: "same tld", FromAddr: "Dot Com <dot@example.com>", Date: time.Now()},
		{ID: 4, UserID: 1, Subject: "same local", FromAddr: "Target <target.sender@example.net>", Date: time.Now()},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "from:target.sender@example.com", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("full email ids = %v", ids)
	}
}

func TestSearchPrioritizesExactSenderAndSubjectCompound(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{
			ID:       1,
			UserID:   1,
			Subject:  "North Star account update",
			FromAddr: "Updates <news@northstar.example>",
			BodyText: "A direct account message.",
			Date:     time.Now(),
		},
		{
			ID:       2,
			UserID:   1,
			Subject:  "Weekly conditions report",
			FromAddr: "reports@example.com",
			BodyText: strings.Repeat("north ", 30) + "one star was visible",
			Date:     time.Now().Add(time.Minute),
		},
		{
			ID:       3,
			UserID:   2,
			Subject:  "North Star account update",
			FromAddr: "news@northstar.example",
			Date:     time.Now(),
		},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "northstar", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 2 {
		t.Fatalf("ids = %v", ids)
	}
	if ids[0] != 1 {
		t.Fatalf("first id = %d, ids = %v", ids[0], ids)
	}
}

func TestSearchBestBlendsRecencyForBroadTerms(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{
			ID:       1,
			UserID:   1,
			Subject:  "Old busy thread",
			BodyText: strings.Repeat("hello ", 200),
			Date:     now.AddDate(-3, 0, 0),
		},
		{
			ID:       2,
			UserID:   1,
			Subject:  "Quick note",
			BodyText: "hello from today",
			Date:     now.Add(-2 * time.Hour),
		},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "hello", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchNormalRecencyPromotesRecentComparableMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Housing Help", BodyText: strings.Repeat("Housing Help housing support ", 30), Date: now.AddDate(-9, 0, 0)},
		{ID: 2, UserID: 1, Subject: "Colorado housing update", BodyText: "housing", Date: now.AddDate(0, -5, 0)},
		{ID: 3, UserID: 1, Subject: "Housing policy this month", BodyText: "housing", Date: now.AddDate(0, 0, -20)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "housing", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 3 || ids[1] != 2 {
		t.Fatalf("normal recency ids = %v", ids)
	}
}

func TestSearchNormalRecencyBeatsOlderSubjectOnlyAdvantage(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "CedarRoot", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, ToAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: "nick", Date: now.AddDate(0, 0, -26)},
		{ID: 2, UserID: 1, Subject: "Nick update", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, ToAddr: "graham@example.test", BodyText: "nick", Date: now.AddDate(0, -10, 0)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 {
		t.Fatalf("normal recency ids = %v", ids)
	}
}

func TestSearchExplicitDateFilterDisablesRecencyBoost(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msg := store.MessageRecord{ID: 1, UserID: 1, Subject: "Checking In", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: "nick", Date: time.Now().UTC()}
	if err := svc.IndexMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}

	query := "nick after:2000-01-01"
	withRecency, ok, err := svc.ScoreMessageWithOptions(ctx, 1, msg.ID, query, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil || !ok {
		t.Fatalf("with recency ok=%v err=%v", ok, err)
	}
	withoutRecency, ok, err := svc.ScoreMessageWithOptions(ctx, 1, msg.ID, query, SearchOptions{Behavior: SearchBehavior{RecencyBias: "none"}})
	if err != nil || !ok {
		t.Fatalf("without recency ok=%v err=%v", ok, err)
	}
	if math.Abs(withRecency-withoutRecency) > 0.000001 {
		t.Fatalf("date-filtered score should not include recency boost: with=%v without=%v", withRecency, withoutRecency)
	}
}

func TestSearchStrongRecencyPutsCurrentSenderAboveOlderTripleFieldMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Re: Graham / Nick Introduction", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: strings.Repeat("nick introduction ", 40), Date: now.AddDate(0, -10, 0)},
		{ID: 2, UserID: 1, Subject: "Re: Checking In", FromAddr: `"Nick Koncilja" <nick@riverrise.com>`, BodyText: "I can talk now. Nick Koncilja", Date: now.Add(-10 * time.Hour)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	rawIDs, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "none", SenderBoost: false, SenderBoostSet: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rawIDs) != 2 || rawIDs[0] != 1 {
		t.Fatalf("raw ids = %v", rawIDs)
	}

	boostedIDs, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "strong", SenderBoost: false, SenderBoostSet: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(boostedIDs) != 2 || boostedIDs[0] != 2 {
		t.Fatalf("strong recency ids = %v", boostedIDs)
	}
}

func TestSearchStrongRecencyOverpowersVeryOldDenseMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Housing Help", BodyText: strings.Repeat("Housing Help housing support ", 300), Date: now.AddDate(-9, 0, 0)},
		{ID: 2, UserID: 1, Subject: "Housing this quarter", BodyText: "housing", Date: now.AddDate(0, -2, 0)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "housing", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "strong"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 {
		t.Fatalf("strong recency ids = %v", ids)
	}
}

func TestSearchBoostsReadSenders(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now()
	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "note", FromAddr: "known@example.com", BodyText: "status", Date: now},
		{ID: 2, UserID: 1, Subject: "note", FromAddr: "other@example.com", BodyText: "status", Date: now},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.SearchWithOptions(ctx, 1, "status", 10, 0, SearchOptions{
		SenderBoosts: []SenderBoost{{Sender: "known@example.com", Boost: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchDateOperators(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	msgs := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "old", BodyText: "invoice", Date: time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "new", BodyText: "invoice", Date: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 1, Subject: "edge", BodyText: "statement", Date: time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC)},
	}
	for _, msg := range msgs {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "invoice after:2025/01/01", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("after ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "invoice before:2025/01/01", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("before ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "year:2025", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("year ids = %v", ids)
	}
}

func TestSearchRelativeDateOperators(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "old reservation", BodyText: "yoga", Date: now.Add(-10 * 24 * time.Hour)},
		{ID: 2, UserID: 1, Subject: "new reservation", BodyText: "yoga", Date: now.Add(-2 * 24 * time.Hour)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := svc.Search(ctx, 1, "yoga older_than:7d", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("older_than ids = %v", ids)
	}
	ids, err = svc.Search(ctx, 1, "yoga newer_than:7d", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("newer_than ids = %v", ids)
	}
}

func TestSearchPlainNegatedTermExcludesMatches(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Longmont errands", BodyText: "Downtown longmont lunch plans", Date: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{ID: 2, UserID: 1, Subject: "Longmont spa", BodyText: "Spavia longmont appointment links", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: 3, UserID: 1, Subject: "Spavia receipt", BodyText: "Spavia links without the city", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 4, UserID: 2, Subject: "Longmont other tenant", BodyText: "No spavia here", Date: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.Search(ctx, 1, "longmont -spavia", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestSearchSenderNameBeatsOlderBodyMentions(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	now := time.Now().UTC()
	today := dayBoundary(now, false)
	messages := []store.MessageRecord{
		{ID: 1, UserID: 1, Subject: "Old notes", BodyText: strings.Repeat("nick ", 80), FromAddr: "Archive <archive@example.test>", Date: today.AddDate(-5, 0, 0)},
		{ID: 2, UserID: 1, Subject: "Checking In", BodyText: "All good. nbk Nick Koncilja", FromAddr: "\"Nick Koncilja\" <nick@riverrise.com>", Date: today.Add(12 * time.Hour)},
	}
	for _, msg := range messages {
		if err := svc.IndexMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.SearchWithOptions(ctx, 1, "nick", 10, 0, SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 2 {
		t.Fatalf("nick ids = %v", ids)
	}

	hit, ok, err := svc.MatchMessageWithOptions(ctx, 1, 2, "nick after:today", SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected sender message to match nick after:today")
	}
	if len(hit.Terms) == 0 {
		t.Fatalf("expected highlight terms for hit: %+v", hit)
	}

	boostedScore, ok, err := svc.ScoreMessageWithOptions(ctx, 1, 2, "nick", SearchOptions{Behavior: SearchBehavior{RecencyBias: "normal"}})
	if err != nil || !ok {
		t.Fatalf("boosted score ok=%v err=%v", ok, err)
	}
	baselineScore, ok, err := svc.ScoreMessageWithOptions(ctx, 1, 2, "nick", SearchOptions{Behavior: SearchBehavior{RecencyBias: "none", SenderBoost: false, SenderBoostSet: true}})
	if err != nil || !ok {
		t.Fatalf("baseline score ok=%v err=%v", ok, err)
	}
	if boostedScore <= baselineScore {
		t.Fatalf("expected recency nudge to raise score: boosted=%v baseline=%v", boostedScore, baselineScore)
	}
}
