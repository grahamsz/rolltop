package syncer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSyncHeartbeatReportsCancelledWorkerWithExhaustedDatabasePool(t *testing.T) {
	fixture := newMoveTestFixture(t)
	db, err := fixture.store.UserDB(context.Background(), fixture.userID)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	diagnostics := newSyncRunDiagnostics()
	ctx = withSyncRunDiagnostics(ctx, diagnostics)
	syncRunPhase(ctx, "sqlite-sync-progress", "")
	// Cancellation must not stop the observer while the worker is still stuck.
	cancel()
	done := make(chan struct{})
	stopped := make(chan struct{})
	logs := make(chan string, 20)
	go func() {
		defer close(stopped)
		runSyncRunHeartbeat(ctx, done, time.Millisecond, fixture.userID, fixture.account.ID, 42,
			diagnostics, db.Stats, func(format string, args ...any) {
				select {
				case logs <- fmt.Sprintf(format, args...):
				default:
				}
			})
	}()
	t.Cleanup(func() {
		close(done)
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("observer did not stop when the worker finished")
		}
	})

	select {
	case line := <-logs:
		for _, want := range []string{
			fmt.Sprintf("user_id=%d account_id=%d run_id=42", fixture.userID, fixture.account.ID),
			`phase="sqlite-sync-progress"`, "context_error=context canceled",
			"db_open=1 db_in_use=1 db_idle=0",
		} {
			if !strings.Contains(line, want) {
				t.Fatalf("heartbeat %q is missing %q", line, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled worker or exhausted SQL pool prevented diagnostics")
	}

	syncRunPhase(ctx, "sqlite-finish-sync-run", "Saving the final sync status")
	timeout := time.After(time.Second)
	for {
		select {
		case line := <-logs:
			if strings.Contains(line, `phase="sqlite-finish-sync-run"`) {
				return
			}
		case <-timeout:
			t.Fatal("observer failed to report the worker's updated phase")
		}
	}
}
