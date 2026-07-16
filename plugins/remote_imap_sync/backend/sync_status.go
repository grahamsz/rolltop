// File overview: Sanitized, throttled lifecycle logging for remote IMAP copy runs.

package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const remoteSyncHeartbeatInterval = 30 * time.Second
const remoteSyncRecoveryWaitHeartbeatInterval = 30 * time.Second

type remoteSyncLogFunc func(string, ...any)

type remoteSyncRunStatus struct {
	ctx               context.Context
	userID            int64
	routineID         int64
	runID             int64
	trigger           string
	heartbeatInterval time.Duration
	logf              remoteSyncLogFunc

	total       atomic.Int64
	scanned     atomic.Int64
	transferred atomic.Int64
	skipped     atomic.Int64
	currentUID  atomic.Uint32
	started     atomic.Bool
	finished    atomic.Bool

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newRemoteSyncRunStatus(ctx context.Context, userID, routineID, runID int64, trigger string) *remoteSyncRunStatus {
	return newRemoteSyncRunStatusWithLogger(ctx, userID, routineID, runID, trigger,
		remoteSyncHeartbeatInterval, log.Printf)
}

func newRemoteSyncRunStatusWithLogger(ctx context.Context, userID, routineID, runID int64, trigger string,
	heartbeatInterval time.Duration, logf remoteSyncLogFunc,
) *remoteSyncRunStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	if logf == nil {
		logf = log.Printf
	}
	status := &remoteSyncRunStatus{
		ctx: ctx, userID: userID, routineID: routineID, runID: runID,
		trigger: safeRemoteSyncTrigger(trigger), heartbeatInterval: heartbeatInterval, logf: logf,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	status.total.Store(-1)
	return status
}

func (s *remoteSyncRunStatus) SetTotal(total int64) {
	if total < 0 {
		total = 0
	}
	s.total.Store(total)
}

func (s *remoteSyncRunStatus) Start() {
	if s == nil || !s.started.CompareAndSwap(false, true) {
		return
	}
	s.logf("remote imap sync run started user_id=%d routine_id=%d run_id=%d trigger=%s pending_uid_total=%s",
		s.userID, s.routineID, s.runID, s.trigger, remoteSyncTotalLogValue(s.total.Load()))
	if s.heartbeatInterval <= 0 {
		close(s.done)
		return
	}
	go s.heartbeatLoop()
}

func (s *remoteSyncRunStatus) Update(scanned, transferred, skipped int64, currentUID uint32) {
	if s == nil {
		return
	}
	s.scanned.Store(scanned)
	s.transferred.Store(transferred)
	s.skipped.Store(skipped)
	s.currentUID.Store(currentUID)
}

func (s *remoteSyncRunStatus) Finish(status string, runErr error) {
	if s == nil || !s.finished.CompareAndSwap(false, true) {
		return
	}
	if s.started.Load() {
		s.stopOnce.Do(func() { close(s.stop) })
		<-s.done
	}
	event := safeRemoteSyncStatus(status)
	format := "remote imap sync run " + event +
		" user_id=%d routine_id=%d run_id=%d trigger=%s scanned=%d transferred=%d skipped=%d current_uid=%d total=%s"
	args := []any{s.userID, s.routineID, s.runID, s.trigger, s.scanned.Load(), s.transferred.Load(),
		s.skipped.Load(), s.currentUID.Load(), remoteSyncTotalLogValue(s.total.Load())}
	if event == "deferred" {
		format += " reason=mailbox_generation_recovery"
	} else if runErr != nil {
		format += " reason=%q"
		args = append(args, sanitizeRemoteError(runErr))
	}
	s.logf(format, args...)
}

func (s *remoteSyncRunStatus) heartbeatLoop() {
	defer close(s.done)
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			s.logHeartbeat()
		}
	}
}

func (s *remoteSyncRunStatus) logHeartbeat() {
	s.logf("remote imap sync run heartbeat user_id=%d routine_id=%d run_id=%d trigger=%s scanned=%d transferred=%d skipped=%d current_uid=%d total=%s",
		s.userID, s.routineID, s.runID, s.trigger, s.scanned.Load(), s.transferred.Load(),
		s.skipped.Load(), s.currentUID.Load(), remoteSyncTotalLogValue(s.total.Load()))
}

func remoteSyncTotalLogValue(total int64) string {
	if total < 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d", total)
}

func safeRemoteSyncTrigger(trigger string) string {
	switch trigger {
	case "startup", "scheduled", "idle", "manual", "recovery", "retry":
		return trigger
	default:
		return "other"
	}
}

func safeRemoteSyncStatus(status string) string {
	switch status {
	case "completed", "deferred", "failed", "canceled":
		return status
	default:
		return "failed"
	}
}

func waitForRemoteSyncRecoveryWithStatus(
	ctx context.Context,
	userID, routineID int64,
	pollInterval, heartbeatInterval time.Duration,
	pending func(context.Context, int64) (bool, error),
	logf remoteSyncLogFunc,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if pending == nil {
		return fmt.Errorf("remote IMAP sync recovery gate is not available")
	}
	if pollInterval <= 0 {
		pollInterval = mailboxGenerationRecoveryPollInterval
	}
	if logf == nil {
		logf = log.Printf
	}
	isPending, err := pending(ctx, userID)
	if err != nil || !isPending {
		return err
	}
	startedAt := time.Now()
	lastHeartbeat := startedAt
	logf("remote imap sync paused user_id=%d routine_id=%d reason=mailbox_generation_recovery",
		userID, routineID)
	for {
		if err := waitForRemoteSyncChunk(ctx, pollInterval); err != nil {
			return err
		}
		isPending, err = pending(ctx, userID)
		if err != nil {
			return err
		}
		now := time.Now()
		if !isPending {
			logf("remote imap sync resumed user_id=%d routine_id=%d reason=mailbox_generation_recovery elapsed=%s",
				userID, routineID, now.Sub(startedAt).Round(time.Second))
			return nil
		}
		if heartbeatInterval > 0 && now.Sub(lastHeartbeat) >= heartbeatInterval {
			logf("remote imap sync pause heartbeat user_id=%d routine_id=%d reason=mailbox_generation_recovery elapsed=%s",
				userID, routineID, now.Sub(startedAt).Round(time.Second))
			lastHeartbeat = now
		}
	}
}
