// File overview: In-memory, metadata-only diagnostics for one bounded mailbox generation recovery turn.

package syncer

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const generationRecoveryHeartbeatInterval = 15 * time.Second

type generationRecoveryDiagnosticsContextKey struct{}

type generationRecoveryDiagnostics struct {
	mu sync.Mutex

	startedAt      time.Time
	phaseStartedAt time.Time
	phase          string
	detail         string
	currentUID     uint32
	checkpointUID  uint32
	turnFetched    int
	turnStored     int
	snapshotTotal  int
}

type generationRecoveryDiagnosticsSnapshot struct {
	startedAt      time.Time
	phaseStartedAt time.Time
	phase          string
	detail         string
	currentUID     uint32
	checkpointUID  uint32
	turnFetched    int
	turnStored     int
	snapshotTotal  int
}

func newGenerationRecoveryDiagnostics(now time.Time) *generationRecoveryDiagnostics {
	if now.IsZero() {
		now = time.Now()
	}
	return &generationRecoveryDiagnostics{
		startedAt:      now,
		phaseStartedAt: now,
		phase:          "starting",
	}
}

func withGenerationRecoveryDiagnostics(ctx context.Context, diagnostics *generationRecoveryDiagnostics) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if diagnostics == nil {
		return ctx
	}
	return context.WithValue(ctx, generationRecoveryDiagnosticsContextKey{}, diagnostics)
}

func generationRecoveryDiagnosticsFromContext(ctx context.Context) *generationRecoveryDiagnostics {
	if ctx == nil {
		return nil
	}
	diagnostics, _ := ctx.Value(generationRecoveryDiagnosticsContextKey{}).(*generationRecoveryDiagnostics)
	return diagnostics
}

func generationRecoveryStartMessage(ctx context.Context, uid uint32) {
	diagnostics := generationRecoveryDiagnosticsFromContext(ctx)
	if diagnostics == nil {
		return
	}
	diagnostics.setPhase("message-received", "", uid)
}

func generationRecoveryPhase(ctx context.Context, phase, detail string) {
	syncRunPhase(ctx, phase, detail)
	diagnostics := generationRecoveryDiagnosticsFromContext(ctx)
	if diagnostics == nil {
		return
	}
	diagnostics.setPhase(phase, detail, 0)
}

func generationRecoverySetTotal(ctx context.Context, total int) {
	diagnostics := generationRecoveryDiagnosticsFromContext(ctx)
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	diagnostics.snapshotTotal = max(total, 0)
	diagnostics.mu.Unlock()
}

func generationRecoveryMessageCompleted(ctx context.Context, uid uint32) {
	diagnostics := generationRecoveryDiagnosticsFromContext(ctx)
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	diagnostics.turnFetched++
	if uid != 0 {
		diagnostics.currentUID = uid
	}
	diagnostics.phase = "message-complete"
	diagnostics.detail = ""
	diagnostics.phaseStartedAt = time.Now()
	diagnostics.mu.Unlock()
}

func generationRecoveryMessageStored(ctx context.Context, uid uint32) {
	diagnostics := generationRecoveryDiagnosticsFromContext(ctx)
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	diagnostics.turnStored++
	if uid != 0 {
		diagnostics.currentUID = uid
	}
	diagnostics.mu.Unlock()
}

func generationRecoveryCheckpoint(ctx context.Context, uid uint32) {
	diagnostics := generationRecoveryDiagnosticsFromContext(ctx)
	if diagnostics == nil {
		return
	}
	diagnostics.mu.Lock()
	diagnostics.checkpointUID = uid
	diagnostics.mu.Unlock()
}

func (d *generationRecoveryDiagnostics) setPhase(phase, detail string, uid uint32) {
	if d == nil {
		return
	}
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = "unknown"
	}
	detail = strings.TrimSpace(detail)
	d.mu.Lock()
	if d.phase != phase || d.detail != detail || (uid != 0 && d.currentUID != uid) {
		d.phaseStartedAt = time.Now()
	}
	d.phase = phase
	d.detail = detail
	if uid != 0 {
		d.currentUID = uid
	}
	d.mu.Unlock()
}

func (d *generationRecoveryDiagnostics) snapshot() generationRecoveryDiagnosticsSnapshot {
	if d == nil {
		return generationRecoveryDiagnosticsSnapshot{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return generationRecoveryDiagnosticsSnapshot{
		startedAt:      d.startedAt,
		phaseStartedAt: d.phaseStartedAt,
		phase:          d.phase,
		detail:         d.detail,
		currentUID:     d.currentUID,
		checkpointUID:  d.checkpointUID,
		turnFetched:    d.turnFetched,
		turnStored:     d.turnStored,
		snapshotTotal:  d.snapshotTotal,
	}
}

func (s generationRecoveryDiagnosticsSnapshot) status(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	phaseElapsed := now.Sub(s.phaseStartedAt).Round(time.Second)
	if phaseElapsed < 0 {
		phaseElapsed = 0
	}
	status := fmt.Sprintf("phase=%q phase_elapsed=%s current_uid=%d checkpoint_uid=%d turn_fetched=%d turn_stored=%d snapshot_total=%d",
		s.phase, phaseElapsed, s.currentUID, s.checkpointUID, s.turnFetched, s.turnStored, s.snapshotTotal)
	if s.detail != "" {
		status += fmt.Sprintf(" detail=%q", s.detail)
	}
	return status
}

func runGenerationRecoveryHeartbeat(
	ctx context.Context,
	done <-chan struct{},
	interval time.Duration,
	userID, accountID int64,
	mailbox string,
	diagnostics *generationRecoveryDiagnostics,
	queueStatus func() string,
	logf func(string, ...any),
) {
	if interval <= 0 || diagnostics == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if logf == nil {
		logf = log.Printf
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case now := <-ticker.C:
			snapshot := diagnostics.snapshot()
			elapsed := now.Sub(snapshot.startedAt).Round(time.Second)
			if elapsed < 0 {
				elapsed = 0
			}
			status := snapshot.status(now)
			if queueStatus != nil {
				if queue := strings.TrimSpace(queueStatus()); queue != "" {
					status += " " + queue
				}
			}
			logf("recover mailbox generation heartbeat user_id=%d account_id=%d mailbox=%q elapsed=%s %s",
				userID, accountID, mailbox, elapsed, status)
		}
	}
}
