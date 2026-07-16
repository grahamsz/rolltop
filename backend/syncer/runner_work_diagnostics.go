// File overview: Metadata-only lifecycle records for scheduler work that can block recovery admission.

package syncer

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	runnerWorkAccountSync        = "account_wide_sync"
	runnerWorkForeground         = "foreground_operation"
	runnerWorkSenderStats        = "sender_stats"
	runnerWorkMailboxSync        = "mailbox_sync"
	runnerWorkMailboxMaintenance = "mailbox_maintenance"
	runnerWorkRecoveryReplay     = "recovery_replay"
	runnerWorkAttachmentIndex    = "attachment_index"
)

type runnerWorkActivity struct {
	kind      string
	phase     string
	userID    int64
	accountID int64
	mailbox   string
	startedAt time.Time
}

func runnerUserWorkActivityKey(kind string, userID int64) string {
	return fmt.Sprintf("user:%d:%s", userID, kind)
}

func runnerMailboxWorkActivityKey(reservationKey string) string {
	return "mailbox:" + reservationKey
}

func (r *Runner) startWorkActivityLocked(key string, activity runnerWorkActivity) {
	if r == nil || strings.TrimSpace(key) == "" || activity.userID <= 0 {
		return
	}
	if r.workActivities == nil {
		r.workActivities = map[string]runnerWorkActivity{}
	}
	if previous, exists := r.workActivities[key]; exists {
		activity.startedAt = previous.startedAt
	}
	if activity.startedAt.IsZero() {
		activity.startedAt = time.Now()
	}
	r.workActivities[key] = activity
}

func (r *Runner) finishWorkActivityLocked(key string) {
	if r == nil || r.workActivities == nil {
		return
	}
	delete(r.workActivities, key)
}

func (r *Runner) startMailboxWorkActivitiesLocked(
	userID, accountID int64,
	mailboxes, keys []string,
	kind string,
) {
	for index, key := range keys {
		mailbox := ""
		if index < len(mailboxes) {
			mailbox = strings.TrimSpace(mailboxes[index])
		}
		r.startWorkActivityLocked(runnerMailboxWorkActivityKey(key), runnerWorkActivity{
			kind:      kind,
			phase:     "reserved",
			userID:    userID,
			accountID: accountID,
			mailbox:   mailbox,
		})
	}
}

func (r *Runner) setMailboxWorkActivitiesPhase(keys []string, phase string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		activityKey := runnerMailboxWorkActivityKey(key)
		activity, ok := r.workActivities[activityKey]
		if !ok {
			continue
		}
		activity.phase = phase
		r.workActivities[activityKey] = activity
	}
}

func (r *Runner) finishMailboxWorkActivitiesLocked(keys []string) {
	for _, key := range keys {
		r.finishWorkActivityLocked(runnerMailboxWorkActivityKey(key))
	}
}

func (r *Runner) ordinaryMailboxContext(userID int64, keys []string, allowPendingRecovery bool) (context.Context, func()) {
	ctx, cancel := context.WithCancel(r.context())
	r.mu.Lock()
	if r.mailboxCancels == nil {
		r.mailboxCancels = map[string]runnerMailboxCancellation{}
	}
	for _, key := range keys {
		r.mailboxCancels[key] = runnerMailboxCancellation{userID: userID, cancel: cancel}
	}
	pending := r.mailboxGenerationRecoveryPendingLocked(userID)
	r.mu.Unlock()
	if pending && !allowPendingRecovery {
		cancel()
	}
	return ctx, func() {
		cancel()
		r.mu.Lock()
		for _, key := range keys {
			delete(r.mailboxCancels, key)
		}
		r.mu.Unlock()
	}
}

func (r *Runner) cancelOrdinaryMailboxWorkLocked(userID int64) {
	if cancel := r.autoCancels[userID]; cancel != nil {
		cancel()
	}
	if cancel := r.senderStatsCancels[userID]; cancel != nil {
		cancel()
	}
	for key, work := range r.mailboxCancels {
		if work.userID != userID || work.cancel == nil {
			continue
		}
		if activityKey := runnerMailboxWorkActivityKey(key); r.workActivities[activityKey].userID == userID {
			activity := r.workActivities[activityKey]
			activity.phase = "yielding_to_recovery"
			r.workActivities[activityKey] = activity
		}
		work.cancel()
	}
}

func (r *Runner) mailboxGenerationRecoveryPending(userID int64) bool {
	if r == nil || userID <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxGenerationRecoveryPendingLocked(userID)
}

func (r *Runner) mailboxGenerationRecoveryPendingLocked(userID int64) bool {
	return r.generationRecoveryUsers[userID] || r.generationRecoveryRuns[userID]
}
