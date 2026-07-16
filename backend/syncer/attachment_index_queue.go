// File overview: Per-tenant attachment-index cursor and failed-message cooldown state.

package syncer

import (
	"fmt"
	"time"
)

const (
	attachmentIndexRetryCooldown            = 5 * time.Minute
	defaultAttachmentIndexContinuationDelay = time.Second
)

type attachmentIndexRetryKey struct {
	userID    int64
	messageID int64
}

// attachmentIndexDeferredError marks message-specific derived-index failures
// that may be skipped without hiding failures in SQLite or Bleve itself.
type attachmentIndexDeferredError struct {
	stage string
	err   error
}

func (e *attachmentIndexDeferredError) Error() string {
	return fmt.Sprintf("attachment indexing deferred during %s", e.stage)
}

func (e *attachmentIndexDeferredError) Unwrap() error {
	return e.err
}

func (s *Service) attachmentIndexCursorForUser(userID int64) int64 {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	return s.attachmentIndexCursor[userID]
}

func (s *Service) advanceAttachmentIndexCursor(userID, messageID int64) {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	if s.attachmentIndexCursor == nil {
		s.attachmentIndexCursor = make(map[int64]int64)
	}
	s.attachmentIndexCursor[userID] = messageID
}

func (s *Service) attachmentIndexRetryReady(userID, messageID int64, now time.Time) bool {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	retryAt := s.attachmentIndexRetryAfter[attachmentIndexRetryKey{userID: userID, messageID: messageID}]
	return retryAt.IsZero() || !now.Before(retryAt)
}

func (s *Service) deferAttachmentIndexRetry(userID, messageID int64, now time.Time) time.Time {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	if s.attachmentIndexRetryAfter == nil {
		s.attachmentIndexRetryAfter = make(map[attachmentIndexRetryKey]time.Time)
	}
	if len(s.attachmentIndexRetryAfter) > 2048 &&
		(s.attachmentIndexLastPrune.IsZero() || now.Sub(s.attachmentIndexLastPrune) >= time.Hour) {
		s.attachmentIndexLastPrune = now
		staleBefore := now.Add(-24 * time.Hour)
		for key, retryAt := range s.attachmentIndexRetryAfter {
			if retryAt.Before(staleBefore) {
				delete(s.attachmentIndexRetryAfter, key)
			}
		}
	}
	retryAt := now.Add(attachmentIndexRetryCooldown)
	s.attachmentIndexRetryAfter[attachmentIndexRetryKey{userID: userID, messageID: messageID}] = retryAt
	return retryAt
}

func (s *Service) clearAttachmentIndexRetry(userID, messageID int64) {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	delete(s.attachmentIndexRetryAfter, attachmentIndexRetryKey{userID: userID, messageID: messageID})
}

func (s *Service) nextAttachmentIndexRetry(userID int64) (time.Time, bool) {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	var earliest time.Time
	for key, retryAt := range s.attachmentIndexRetryAfter {
		if key.userID != userID || (!earliest.IsZero() && !retryAt.Before(earliest)) {
			continue
		}
		earliest = retryAt
	}
	return earliest, !earliest.IsZero()
}

func (s *Service) deferAttachmentIndexContinuation(userID int64, now time.Time) time.Time {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	delay := s.attachmentIndexContinuationDelay
	if delay <= 0 {
		delay = defaultAttachmentIndexContinuationDelay
	}
	continueAt := now.Add(delay)
	if s.attachmentIndexContinueAt == nil {
		s.attachmentIndexContinueAt = make(map[int64]time.Time)
	}
	if existing := s.attachmentIndexContinueAt[userID]; !existing.IsZero() && existing.Before(continueAt) {
		return existing
	}
	s.attachmentIndexContinueAt[userID] = continueAt
	return continueAt
}

func (s *Service) attachmentIndexContinuationBlocked(userID int64, now time.Time) (time.Time, bool) {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	continueAt := s.attachmentIndexContinueAt[userID]
	if continueAt.IsZero() {
		return time.Time{}, false
	}
	if !continueAt.After(now) {
		delete(s.attachmentIndexContinueAt, userID)
		return time.Time{}, false
	}
	return continueAt, true
}

func (s *Service) nextAttachmentIndexWake(userID int64) (time.Time, bool) {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	earliest := s.attachmentIndexContinueAt[userID]
	for key, retryAt := range s.attachmentIndexRetryAfter {
		if key.userID != userID || (!earliest.IsZero() && !retryAt.Before(earliest)) {
			continue
		}
		earliest = retryAt
	}
	return earliest, !earliest.IsZero()
}

func (s *Service) releaseDueAttachmentIndexWakes(userID int64, now time.Time) {
	s.attachmentIndexMu.Lock()
	defer s.attachmentIndexMu.Unlock()
	for key, retryAt := range s.attachmentIndexRetryAfter {
		if key.userID == userID && !retryAt.After(now) {
			delete(s.attachmentIndexRetryAfter, key)
		}
	}
	if continueAt := s.attachmentIndexContinueAt[userID]; !continueAt.IsZero() && !continueAt.After(now) {
		delete(s.attachmentIndexContinueAt, userID)
	}
}

func (r *Runner) scheduleNextAttachmentIndexRetry(userID int64) {
	if r == nil || r.Service == nil || userID <= 0 {
		return
	}
	retryAt, ok := r.Service.nextAttachmentIndexWake(userID)
	if !ok {
		retryAt = time.Time{}
	}
	r.scheduleAttachmentIndexRetry(userID, retryAt)
}

func (r *Runner) scheduleAttachmentIndexRetry(userID int64, retryAt time.Time) {
	if r == nil || userID <= 0 {
		return
	}
	r.mu.Lock()
	r.cancelAttachmentRetryTimerLocked(userID)
	if retryAt.IsZero() || r.context().Err() != nil {
		r.mu.Unlock()
		return
	}
	if r.attachmentRetryTimers == nil {
		r.attachmentRetryTimers = make(map[int64]*time.Timer)
	}
	epoch := r.attachmentRetryEpoch[userID]
	delay := time.Until(retryAt)
	if delay < 0 {
		delay = 0
	}
	r.attachmentRetryTimers[userID] = time.AfterFunc(delay, func() {
		r.mu.Lock()
		if r.attachmentRetryEpoch[userID] != epoch {
			r.mu.Unlock()
			return
		}
		delete(r.attachmentRetryTimers, userID)
		r.attachmentRetryEpoch[userID]++
		r.mu.Unlock()
		if r.context().Err() == nil {
			if r.Service != nil {
				r.Service.releaseDueAttachmentIndexWakes(userID, time.Now())
			}
			r.StartAttachmentIndex(userID)
		}
	})
	r.mu.Unlock()
}

func (r *Runner) cancelAttachmentRetryTimerLocked(userID int64) {
	if timer := r.attachmentRetryTimers[userID]; timer != nil {
		timer.Stop()
		delete(r.attachmentRetryTimers, userID)
	}
	if r.attachmentRetryEpoch == nil {
		r.attachmentRetryEpoch = make(map[int64]uint64)
	}
	r.attachmentRetryEpoch[userID]++
}

func (r *Runner) cancelAllAttachmentRetryTimers() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for userID, timer := range r.attachmentRetryTimers {
		if timer != nil {
			timer.Stop()
		}
		delete(r.attachmentRetryTimers, userID)
		r.attachmentRetryEpoch[userID]++
	}
}
