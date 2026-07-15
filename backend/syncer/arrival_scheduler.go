// File overview: Deduplicated delayed finalization for Inbox arrivals awaiting move correlation.

package syncer

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"rolltop/backend/store"
)

const (
	inboxArrivalRetryDelay                   = 30 * time.Second
	inboxArrivalMinimumDelay                 = 10 * time.Millisecond
	mailboxGenerationRebuildRecoveryInterval = 30 * time.Second
)

type inboxArrivalTimer interface {
	Stop() bool
}

type inboxArrivalTimerFactory func(delay time.Duration, callback func()) inboxArrivalTimer

type inboxArrivalKey struct {
	userID    int64
	accountID int64
}

type inboxArrivalSchedule struct {
	due        time.Time
	timer      inboxArrivalTimer
	generation uint64
	running    bool
}

type inboxArrivalScheduler struct {
	ctx      context.Context
	finalize func(context.Context, int64, int64, time.Time) (int, time.Time, error)
	notify   func(int64)
	now      func() time.Time
	after    inboxArrivalTimerFactory

	mu           sync.Mutex
	schedules    map[inboxArrivalKey]*inboxArrivalSchedule
	nextSequence uint64
	watchOnce    sync.Once
}

func newInboxArrivalScheduler(
	ctx context.Context,
	finalize func(context.Context, int64, int64, time.Time) (int, time.Time, error),
	notify func(int64),
) *inboxArrivalScheduler {
	if ctx == nil {
		ctx = context.Background()
	}
	return &inboxArrivalScheduler{
		ctx:       ctx,
		finalize:  finalize,
		notify:    notify,
		now:       time.Now,
		after:     func(delay time.Duration, callback func()) inboxArrivalTimer { return time.AfterFunc(delay, callback) },
		schedules: map[inboxArrivalKey]*inboxArrivalSchedule{},
	}
}

// ScheduleInboxArrival keeps the earliest known deadline for one tenant and
// account. Repeated discoveries share one timer and one finalizer invocation.
func (r *Runner) ScheduleInboxArrival(userID, accountID int64, due time.Time) {
	if r == nil || r.arrivalScheduler == nil {
		return
	}
	r.arrivalScheduler.schedule(userID, accountID, due)
}

// RecoverPendingInboxArrivals restores durable timers after process startup.
// The store returns one earliest deadline per tenant/account, so scheduling is
// bounded by the number of configured accounts rather than message count.
func (r *Runner) RecoverPendingInboxArrivals() error {
	if r == nil || r.Service == nil || r.Service.Store == nil {
		return nil
	}
	ctx := r.context()
	if err := ctx.Err(); err != nil {
		return err
	}
	rebuildCount, err := r.queuePendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		return err
	}
	if rebuildCount > 0 {
		r.startMailboxGenerationRebuildRecovery()
	}
	schedules, err := r.Service.Store.ListPendingInboxArrivalSchedules(ctx)
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		r.ScheduleInboxArrival(schedule.UserID, schedule.AccountID, schedule.DueAt)
	}
	return nil
}

func (r *Runner) queuePendingMailboxGenerationRebuilds(ctx context.Context) (int, error) {
	epochSnapshot := r.generationRecoveryEpochSnapshot()
	rebuilds, err := r.Service.Store.ListPendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		return 0, err
	}
	pendingUsers := make(map[int64]bool, len(rebuilds))
	pendingAccounts := make(map[int64]map[int64]bool, len(rebuilds))
	for _, rebuild := range rebuilds {
		pendingUsers[rebuild.UserID] = true
		if pendingAccounts[rebuild.UserID] == nil {
			pendingAccounts[rebuild.UserID] = map[int64]bool{}
		}
		pendingAccounts[rebuild.UserID][rebuild.AccountID] = true
	}
	r.reconcileGenerationRecoveryUsers(pendingUsers, pendingAccounts, epochSnapshot)
	for _, replay := range r.takeReadyGenerationRecoveryAccountReplays() {
		r.QueueAccountMailboxes(replay.userID, replay.request.accountID, []string{replay.request.mailbox})
	}
	// Preserve Inbox-first ordering within each account, but rotate the account
	// attempted for each tenant so one failing server cannot monopolize recovery.
	for _, rebuild := range r.nextMailboxGenerationRecoveryAttempts(rebuilds) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if r.queueRebuildMailbox != nil {
			r.queueRebuildMailbox(rebuild)
			r.markMailboxGenerationRecoveryAttempt(rebuild)
			continue
		}
		if r.startPendingMailboxGenerationRebuild(rebuild) {
			r.markMailboxGenerationRecoveryAttempt(rebuild)
		}
	}
	return len(rebuilds), nil
}

func (r *Runner) startPendingMailboxGenerationRebuild(rebuild store.PendingMailboxGenerationRebuild) bool {
	keys, reserved := r.reserveGenerationRecoveryMailbox(rebuild)
	if !reserved {
		return false
	}
	go func() {
		succeeded := false
		defer func() {
			r.releaseGenerationRecoveryMailbox(rebuild.UserID, keys)
			if r.context().Err() != nil {
				return
			}
			if succeeded {
				r.refreshGenerationRecoveryGateForUser(r.context(), rebuild.UserID)
				return
			}
			// The recovery loop already polls durable markers. Do not turn a
			// persistent IMAP failure into an unbounded immediate retry loop.
			r.startMailboxGenerationRebuildRecovery()
		}()
		if _, err := r.Service.SyncUserAccountMailboxes(r.context(), rebuild.UserID, rebuild.AccountID,
			[]string{rebuild.MailboxName}); err != nil {
			log.Printf("recover mailbox generation user_id=%d account_id=%d mailbox=%s: %v",
				rebuild.UserID, rebuild.AccountID, rebuild.MailboxName, err)
			return
		}
		succeeded = true
	}()
	return true
}

func (r *Runner) startMailboxGenerationRebuildRecovery() {
	r.startMailboxGenerationRebuildRecoveryLoop(false)
}

func (r *Runner) startMailboxGenerationRebuildRecoveryLoop(wakeNow bool) {
	if r == nil || r.Service == nil || r.Service.Store == nil || r.context().Err() != nil {
		return
	}
	r.mu.Lock()
	if r.rebuildRecoveryWake == nil {
		r.rebuildRecoveryWake = make(chan struct{}, 1)
	}
	if wakeNow {
		select {
		case r.rebuildRecoveryWake <- struct{}{}:
		default:
		}
	}
	if r.rebuildRecoveryRunning {
		r.mu.Unlock()
		return
	}
	r.rebuildRecoveryRunning = true
	interval := r.rebuildRecoveryInterval
	if interval <= 0 {
		interval = mailboxGenerationRebuildRecoveryInterval
	}
	wake := r.rebuildRecoveryWake
	r.mu.Unlock()

	go func(wake <-chan struct{}) {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		scanImmediately := false
		for {
			if scanImmediately {
				scanImmediately = false
			} else {
				select {
				case <-r.context().Done():
					r.finishMailboxGenerationRebuildRecoveryLoop()
					return
				case <-timer.C:
				case <-wake:
				}
			}
			remaining, err := r.queuePendingMailboxGenerationRebuilds(r.context())
			if err != nil {
				if r.context().Err() != nil {
					r.finishMailboxGenerationRebuildRecoveryLoop()
					return
				}
				log.Printf("recover mailbox generation rebuilds: %v", err)
				resetRecoveryTimer(timer, interval)
				continue
			}
			if remaining == 0 {
				if r.rebuildRecoveryBeforeStop != nil {
					r.rebuildRecoveryBeforeStop()
				}
				r.mu.Lock()
				select {
				case <-wake:
					r.mu.Unlock()
					resetRecoveryTimer(timer, interval)
					scanImmediately = true
					continue
				default:
					if len(r.generationRecoveryUsers) > 0 || len(r.generationRecoveryRuns) > 0 {
						r.mu.Unlock()
						resetRecoveryTimer(timer, interval)
						continue
					}
					r.rebuildRecoveryRunning = false
					r.mu.Unlock()
					return
				}
			}
			resetRecoveryTimer(timer, interval)
		}
	}(wake)
}

func (r *Runner) wakeMailboxGenerationRebuildRecovery() {
	r.startMailboxGenerationRebuildRecoveryLoop(true)
}

func (r *Runner) finishMailboxGenerationRebuildRecoveryLoop() {
	r.mu.Lock()
	r.rebuildRecoveryRunning = false
	r.mu.Unlock()
}

func resetRecoveryTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}

func (r *Runner) finalizePendingInboxArrivals(ctx context.Context, userID, accountID int64, now time.Time) (int, time.Time, error) {
	if r == nil || r.Service == nil {
		return 0, time.Time{}, fmt.Errorf("sync service is not configured")
	}
	return r.Service.FinalizePendingInboxArrivals(ctx, userID, accountID, now)
}

func (r *Runner) notifyInboxArrivals(userID int64) {
	if r != nil && r.Service != nil {
		r.Service.notify(userID)
	}
}

func (s *inboxArrivalScheduler) schedule(userID, accountID int64, due time.Time) bool {
	if s == nil || userID <= 0 || accountID <= 0 || due.IsZero() || s.ctx.Err() != nil {
		return false
	}
	due = due.UTC()
	key := inboxArrivalKey{userID: userID, accountID: accountID}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.startCancellationWatcherLocked()
	entry := s.schedules[key]
	if entry == nil {
		entry = &inboxArrivalSchedule{}
		s.schedules[key] = entry
	}
	if !entry.due.IsZero() && !due.Before(entry.due) {
		return false
	}
	entry.due = due
	if entry.running {
		return true
	}
	s.armLocked(key, entry)
	return true
}

func (s *inboxArrivalScheduler) armLocked(key inboxArrivalKey, entry *inboxArrivalSchedule) {
	if entry.timer != nil {
		entry.timer.Stop()
	}
	s.nextSequence++
	entry.generation = s.nextSequence
	generation := entry.generation
	delay := entry.due.Sub(s.now().UTC())
	if delay < inboxArrivalMinimumDelay {
		delay = inboxArrivalMinimumDelay
	}
	entry.timer = s.after(delay, func() {
		s.fire(key, generation)
	})
}

func (s *inboxArrivalScheduler) fire(key inboxArrivalKey, generation uint64) {
	s.mu.Lock()
	entry := s.schedules[key]
	if entry == nil || entry.generation != generation || entry.running || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	entry.timer = nil
	entry.running = true
	entry.due = time.Time{}
	s.mu.Unlock()

	now := s.now().UTC()
	created, nextDue, err := s.finalize(s.ctx, key.userID, key.accountID, now)
	if err != nil && s.ctx.Err() == nil {
		log.Printf("finalize pending Inbox arrivals user_id=%d account_id=%d: %v", key.userID, key.accountID, err)
	}
	if created > 0 && s.notify != nil && s.ctx.Err() == nil {
		s.notify(key.userID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry = s.schedules[key]
	if entry == nil || entry.generation != generation {
		return
	}
	entry.running = false
	if s.ctx.Err() != nil {
		delete(s.schedules, key)
		return
	}
	if err != nil {
		retryDue := s.now().UTC().Add(inboxArrivalRetryDelay)
		if entry.due.IsZero() || retryDue.Before(entry.due) {
			entry.due = retryDue
		}
	} else if !nextDue.IsZero() && (entry.due.IsZero() || nextDue.Before(entry.due)) {
		entry.due = nextDue.UTC()
	}
	if entry.due.IsZero() {
		delete(s.schedules, key)
		return
	}
	s.armLocked(key, entry)
}

func (s *inboxArrivalScheduler) startCancellationWatcherLocked() {
	if s.ctx.Done() == nil {
		return
	}
	s.watchOnce.Do(func() {
		go func() {
			<-s.ctx.Done()
			s.mu.Lock()
			for key, entry := range s.schedules {
				if entry.timer != nil {
					entry.timer.Stop()
				}
				delete(s.schedules, key)
			}
			s.mu.Unlock()
		}()
	})
}
