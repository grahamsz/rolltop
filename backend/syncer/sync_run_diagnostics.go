package syncer

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"time"
)

// syncRunDiagnostics is deliberately in-memory. It describes the exact live
// step for an executing run; durable counters remain in sync_runs.
type syncRunDiagnostics struct {
	mu             sync.Mutex
	phase          string
	detail         string
	phaseStartedAt time.Time
}

type syncRunDiagnosticsContextKey struct{}

func newSyncRunDiagnostics() *syncRunDiagnostics {
	return &syncRunDiagnostics{phase: "starting", phaseStartedAt: time.Now()}
}

func withSyncRunDiagnostics(ctx context.Context, diagnostics *syncRunDiagnostics) context.Context {
	if diagnostics == nil {
		return ctx
	}
	return context.WithValue(ctx, syncRunDiagnosticsContextKey{}, diagnostics)
}

func syncRunPhase(ctx context.Context, phase, detail string) {
	diagnostics, _ := ctx.Value(syncRunDiagnosticsContextKey{}).(*syncRunDiagnostics)
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	diagnostics.phase = strings.TrimSpace(phase)
	diagnostics.detail = strings.TrimSpace(detail)
	diagnostics.phaseStartedAt = time.Now()
	diagnostics.mu.Unlock()
}

func (d *syncRunDiagnostics) snapshot() (string, string, time.Time) {
	if d == nil {
		return "", "", time.Time{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.phase, d.detail, d.phaseStartedAt
}

// Keep reporting until the worker actually returns, including after its context
// expires. A cancellation request alone does not prove a blocked call yielded.
func (s *Service) watchSyncRun(ctx context.Context, userID, accountID, runID int64) (context.Context, func()) {
	diagnostics, _ := ctx.Value(syncRunDiagnosticsContextKey{}).(*syncRunDiagnostics)
	if diagnostics == nil {
		diagnostics = newSyncRunDiagnostics()
		ctx = withSyncRunDiagnostics(ctx, diagnostics)
	}
	db, err := s.Store.UserDB(ctx, userID)
	if err != nil {
		return ctx, func() {}
	}
	done := make(chan struct{})
	go runSyncRunHeartbeat(ctx, done, time.Minute, userID, accountID, runID, diagnostics, db.Stats, log.Printf)
	return ctx, func() { close(done) }
}

func runSyncRunHeartbeat(ctx context.Context, done <-chan struct{}, interval time.Duration,
	userID, accountID, runID int64, diagnostics *syncRunDiagnostics,
	dbStats func() sql.DBStats, logf func(string, ...any),
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	started := time.Now()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			// Prefer completion if it raced the ticker.
			select {
			case <-done:
				return
			default:
			}
			phase, detail, phaseStarted := diagnostics.snapshot()
			stats := dbStats() // In-memory counters; never query the busy database.
			logf("sync worker heartbeat user_id=%d account_id=%d run_id=%d elapsed=%s phase=%q phase_elapsed=%s detail=%q context_error=%v db_max=%d db_open=%d db_in_use=%d db_idle=%d db_wait_count=%d db_wait_duration=%s",
				userID, accountID, runID, now.Sub(started).Round(time.Second), phase,
				now.Sub(phaseStarted).Round(time.Second), detail, ctx.Err(),
				stats.MaxOpenConnections, stats.OpenConnections, stats.InUse, stats.Idle, stats.WaitCount, stats.WaitDuration.Round(time.Second))
		}
	}
}
