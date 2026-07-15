// File overview: Deduplicated delayed finalization for Inbox arrivals awaiting move correlation.

package syncer

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
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
	rebuilds, err := r.Service.Store.ListPendingMailboxGenerationRebuilds(ctx)
	if err != nil {
		return 0, err
	}
	for _, rebuild := range rebuilds {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if r.queueRebuildMailbox != nil {
			r.queueRebuildMailbox(rebuild)
			continue
		}
		r.QueueAccountMailboxes(rebuild.UserID, rebuild.AccountID, []string{rebuild.MailboxName})
	}
	return len(rebuilds), nil
}

func (r *Runner) startMailboxGenerationRebuildRecovery() {
	if r == nil || r.Service == nil || r.Service.Store == nil || r.context().Err() != nil {
		return
	}
	r.mu.Lock()
	if r.rebuildRecoveryRunning {
		r.mu.Unlock()
		return
	}
	r.rebuildRecoveryRunning = true
	interval := r.rebuildRecoveryInterval
	if interval <= 0 {
		interval = mailboxGenerationRebuildRecoveryInterval
	}
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			r.rebuildRecoveryRunning = false
			r.mu.Unlock()
		}()
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-r.context().Done():
				return
			case <-timer.C:
			}
			remaining, err := r.queuePendingMailboxGenerationRebuilds(r.context())
			if err != nil {
				if r.context().Err() != nil {
					return
				}
				log.Printf("recover mailbox generation rebuilds: %v", err)
				timer.Reset(interval)
				continue
			}
			if remaining == 0 {
				return
			}
			timer.Reset(interval)
		}
	}()
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
