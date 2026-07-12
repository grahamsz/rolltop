// File overview: Startup recovery and precise wake scheduling for local snooze reminders.

package web

import (
	"context"
	"errors"
	"log"
	"time"
)

const (
	snoozeSchedulerIdleInterval = time.Hour
	snoozeSchedulerErrorBackoff = 30 * time.Second
	snoozeSchedulerMinimumDelay = 100 * time.Millisecond
	snoozeSchedulerProcessLimit = 100
)

func (s *Server) startSnoozeScheduler() {
	if s == nil || s.store == nil {
		return
	}
	if s.snoozeSchedulerWake == nil {
		s.snoozeSchedulerWake = make(chan struct{}, 1)
	}
	go s.runSnoozeScheduler()
}

func (s *Server) runSnoozeScheduler() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-s.snoozeSchedulerWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		now := time.Now().UTC()
		next, err := s.processDueSnoozes(context.Background(), now)
		delay := snoozeSchedulerIdleInterval
		if err != nil {
			log.Printf("snooze scheduler: %v", err)
			delay = snoozeSchedulerErrorBackoff
		} else if !next.IsZero() {
			delay = time.Until(next)
			if delay < snoozeSchedulerMinimumDelay {
				delay = snoozeSchedulerMinimumDelay
			}
		}
		timer.Reset(delay)
	}
}

// processDueSnoozes recovers durable pending state for every local user. It
// returns the earliest remaining due time so the caller can sleep precisely.
func (s *Server) processDueSnoozes(ctx context.Context, now time.Time) (time.Time, error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	var firstErr error
	for _, user := range users {
		events, err := s.store.RecordDueSnoozeReminderEvents(ctx, user.ID, now, snoozeSchedulerProcessLimit)
		if err != nil {
			if !errors.Is(err, context.Canceled) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(events) > 0 {
			s.noteMailListChanged(user.ID)
			s.warmAllMailFirstPageAsync(user.ID)
			s.notifySnoozeReminderWebPushAsync(user.ID)
			if s.events != nil {
				s.events.Notify(user.ID)
			}
		}
		userNext, err := s.store.NextPendingSnoozeDue(ctx, user.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !userNext.IsZero() && (next.IsZero() || userNext.Before(next)) {
			next = userNext
		}
	}
	return next, firstErr
}

func (s *Server) notifySnoozeStateChanged(userID int64) {
	if s == nil || userID <= 0 {
		return
	}
	s.noteMailListChanged(userID)
	s.warmAllMailFirstPageAsync(userID)
	if s.events != nil {
		s.events.Notify(userID)
	}
	if s.snoozeSchedulerWake != nil {
		select {
		case s.snoozeSchedulerWake <- struct{}{}:
		default:
		}
	}
}
