// File overview: Rate-limited, metadata-only status for durable mailbox generation recovery queues.

package syncer

import (
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"rolltop/backend/store"
)

const generationRecoveryQueueStatusRepeatInterval = 2 * time.Minute
const generationRecoveryQueueStatusTargetLimit = 8

type generationRecoveryQueueTarget struct {
	accountID int64
	mailboxID int64
	mailbox   string
}

type generationRecoveryQueueLogState struct {
	signature string
	loggedAt  time.Time
}

type generationRecoveryQueueSnapshot struct {
	pending                []generationRecoveryQueueTarget
	active                 *generationRecoveryQueueTarget
	otherMailboxWorkActive bool
}

// updateGenerationRecoveryQueueStatuses installs one durable marker snapshot
// and emits immediately when its active/queued shape changes. An unchanged
// queue is repeated only occasionally; active turns also include this summary
// in their existing 15-second heartbeat.
func (r *Runner) updateGenerationRecoveryQueueStatuses(
	rebuilds []store.PendingMailboxGenerationRebuild,
	now time.Time,
	logf func(string, ...any),
) {
	if r == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if logf == nil {
		logf = log.Printf
	}

	pendingByUser := make(map[int64][]generationRecoveryQueueTarget)
	for _, rebuild := range rebuilds {
		if rebuild.UserID <= 0 || rebuild.AccountID <= 0 || strings.TrimSpace(rebuild.MailboxName) == "" {
			continue
		}
		pendingByUser[rebuild.UserID] = append(pendingByUser[rebuild.UserID], generationRecoveryQueueTarget{
			accountID: rebuild.AccountID,
			mailboxID: rebuild.MailboxID,
			mailbox:   rebuild.MailboxName,
		})
	}
	for userID := range pendingByUser {
		sortGenerationRecoveryQueueTargets(pendingByUser[userID])
	}

	type queuedLog struct {
		userID int64
		status string
	}
	var lines []queuedLog

	r.mu.Lock()
	if r.generationRecoveryQueues == nil {
		r.generationRecoveryQueues = map[int64][]generationRecoveryQueueTarget{}
	}
	if r.generationRecoveryQueueLog == nil {
		r.generationRecoveryQueueLog = map[int64]generationRecoveryQueueLogState{}
	}
	userSet := make(map[int64]bool, len(pendingByUser)+len(r.generationRecoveryQueues)+
		len(r.generationRecoveryQueueLog)+len(r.generationRecoveryActive))
	for userID := range pendingByUser {
		userSet[userID] = true
	}
	for userID := range r.generationRecoveryQueues {
		userSet[userID] = true
	}
	// Keep users with an active turn or a previously emitted line in the next
	// snapshot even if their durable marker disappeared first. Otherwise the
	// eventual active-to-drained transition can be lost.
	for userID := range r.generationRecoveryActive {
		userSet[userID] = true
	}
	for userID := range r.generationRecoveryQueueLog {
		userSet[userID] = true
	}
	userIDs := make([]int64, 0, len(userSet))
	for userID := range userSet {
		userIDs = append(userIDs, userID)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })

	for _, userID := range userIDs {
		hadQueue := len(r.generationRecoveryQueues[userID]) > 0
		pending := cloneGenerationRecoveryQueueTargets(pendingByUser[userID])
		if len(pending) > 0 {
			r.generationRecoveryQueues[userID] = pending
		} else {
			delete(r.generationRecoveryQueues, userID)
		}
		snapshot := r.generationRecoveryQueueSnapshotLocked(userID, pending)
		status := snapshot.status()
		previous, previouslyLogged := r.generationRecoveryQueueLog[userID]
		changed := !previouslyLogged || previous.signature != status
		repeatDue := previouslyLogged && now.Sub(previous.loggedAt) >= generationRecoveryQueueStatusRepeatInterval
		if changed || repeatDue {
			// Do not introduce a zero-state line for users that never had a
			// marker. A final zero line is useful when a known queue drains.
			if len(pending) > 0 || hadQueue || previouslyLogged || snapshot.active != nil {
				lines = append(lines, queuedLog{userID: userID, status: status})
				r.generationRecoveryQueueLog[userID] = generationRecoveryQueueLogState{
					signature: status,
					loggedAt:  now,
				}
			}
		}
		if len(pending) == 0 && snapshot.active == nil {
			delete(r.generationRecoveryQueueLog, userID)
		}
	}
	r.mu.Unlock()

	for _, line := range lines {
		logf("recover mailbox generation queue user_id=%d %s", line.userID, line.status)
	}
}

func (r *Runner) generationRecoveryQueueStatusForUser(userID int64) string {
	if r == nil || userID <= 0 {
		return "pending_markers=0 active_target=none queued_targets=[] other_mailbox_work_active=false"
	}
	r.mu.Lock()
	snapshot := r.generationRecoveryQueueSnapshotLocked(userID, r.generationRecoveryQueues[userID])
	r.mu.Unlock()
	return snapshot.status()
}

func (r *Runner) generationRecoveryQueueSnapshotLocked(
	userID int64,
	pending []generationRecoveryQueueTarget,
) generationRecoveryQueueSnapshot {
	snapshot := generationRecoveryQueueSnapshot{pending: cloneGenerationRecoveryQueueTargets(pending)}
	if activity, ok := r.generationRecoveryActive[userID]; ok {
		active := generationRecoveryQueueTarget{accountID: activity.accountID, mailbox: activity.mailbox}
		snapshot.active = &active
	}
	snapshot.otherMailboxWorkActive = r.generationRecoveryOtherMailboxWorkActiveLocked(userID)
	return snapshot
}

func (r *Runner) generationRecoveryOtherMailboxWorkActiveLocked(userID int64) bool {
	// Report every user-scoped writer that can keep the next recovery turn from
	// being admitted, including the small gaps between account-wide folders.
	if r.autoRunning[userID] || r.autoPlanning[userID] || r.foregroundRunning[userID] > 0 ||
		r.senderStatsRunning[userID] || r.attachmentDone[userID] != nil {
		return true
	}
	prefix := mailboxKey(userID, "")
	attachmentKey := mailboxKey(userID, "__attachments__")
	activeKey := ""
	if activity, ok := r.generationRecoveryActive[userID]; ok {
		activeKey = accountMailboxKey(userID, activity.accountID, activity.mailbox)
	}
	for key := range r.mailboxRunning {
		if key != attachmentKey && key != activeKey && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (s generationRecoveryQueueSnapshot) status() string {
	active := "none"
	if s.active != nil {
		active = s.active.status()
	}
	queued := make([]string, 0, len(s.pending))
	activeRemoved := false
	for _, target := range s.pending {
		if !activeRemoved && s.active != nil && target.sameMailbox(*s.active) {
			activeRemoved = true
			continue
		}
		queued = append(queued, target.status())
	}
	omitted := 0
	if len(queued) > generationRecoveryQueueStatusTargetLimit {
		omitted = len(queued) - generationRecoveryQueueStatusTargetLimit
		queued = queued[:generationRecoveryQueueStatusTargetLimit]
	}
	status := fmt.Sprintf("pending_markers=%d active_target=%s queued_targets=[%s]",
		len(s.pending), active, strings.Join(queued, " "))
	if omitted > 0 {
		status += fmt.Sprintf(" queued_targets_omitted=%d", omitted)
	}
	status += fmt.Sprintf(" other_mailbox_work_active=%t", s.otherMailboxWorkActive)
	return status
}

func (t generationRecoveryQueueTarget) status() string {
	return fmt.Sprintf("{account_id=%d mailbox=%s}", t.accountID, strconv.Quote(t.mailbox))
}

func (t generationRecoveryQueueTarget) sameMailbox(other generationRecoveryQueueTarget) bool {
	return t.accountID == other.accountID &&
		strings.EqualFold(strings.TrimSpace(t.mailbox), strings.TrimSpace(other.mailbox))
}

func sortGenerationRecoveryQueueTargets(targets []generationRecoveryQueueTarget) {
	sort.Slice(targets, func(i, j int) bool {
		left, right := targets[i], targets[j]
		if left.accountID != right.accountID {
			return left.accountID < right.accountID
		}
		leftMailbox := strings.ToLower(strings.TrimSpace(left.mailbox))
		rightMailbox := strings.ToLower(strings.TrimSpace(right.mailbox))
		if leftMailbox != rightMailbox {
			return leftMailbox < rightMailbox
		}
		if left.mailbox != right.mailbox {
			return left.mailbox < right.mailbox
		}
		return left.mailboxID < right.mailboxID
	})
}

func cloneGenerationRecoveryQueueTargets(targets []generationRecoveryQueueTarget) []generationRecoveryQueueTarget {
	if len(targets) == 0 {
		return nil
	}
	return append([]generationRecoveryQueueTarget(nil), targets...)
}
