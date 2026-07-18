package syncer

import (
	"context"
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
