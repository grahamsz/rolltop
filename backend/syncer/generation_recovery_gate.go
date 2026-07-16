// File overview: Tenant-scoped scheduling gate for crash-resumable mailbox generation recovery.

package syncer

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"rolltop/backend/store"
)

type deferredAccountMailbox struct {
	accountID int64
	mailbox   string
}

type generationRecoveryReplay struct {
	userID           int64
	auto             bool
	mailboxes        []string
	accountMailboxes []deferredAccountMailbox
	senderStats      bool
	attachments      bool
}

type generationRecoveryActivity struct {
	accountID   int64
	mailbox     string
	startedAt   time.Time
	diagnostics *generationRecoveryDiagnostics
}

// SignalMailboxGenerationRecovery closes the tenant gate as soon as a normal
// sync creates a durable generation marker. The epoch prevents an older store
// snapshot from reopening a gate that was signaled while that query ran.
func (r *Runner) SignalMailboxGenerationRecovery(userID int64) {
	if r == nil || userID <= 0 || r.context().Err() != nil {
		return
	}
	r.mu.Lock()
	r.ensureGenerationRecoveryMapsLocked()
	r.generationRecoveryEpoch[userID]++
	delete(r.generationRecoveryAccounts, userID)
	delete(r.generationRecoveryTargets, userID)
	delete(r.generationRecoveryKnown, userID)
	r.activateGenerationRecoveryLocked(userID)
	r.mu.Unlock()
	r.wakeMailboxGenerationRebuildRecovery()
}

// MailboxGenerationRecoveryActive reports the process-local tenant gate used
// by backend plugins. Durable store markers remain authoritative across a
// restart; this covers active rebuilds and deferred replay after marker clear.
func (r *Runner) MailboxGenerationRecoveryActive(userID int64) bool {
	if r == nil || userID <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generationRecoveryGatedLocked(userID) || r.generationRecoveryRuns[userID]
}

func (r *Runner) generationRecoveryEpochSnapshot() map[int64]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]uint64, len(r.generationRecoveryUsers))
	for userID := range r.generationRecoveryUsers {
		out[userID] = r.generationRecoveryEpoch[userID]
	}
	return out
}

func (r *Runner) generationRecoveryEpochSnapshotForUser(userID int64) map[int64]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]uint64{}
	if r.generationRecoveryUsers[userID] {
		out[userID] = r.generationRecoveryEpoch[userID]
	}
	return out
}

func (r *Runner) reconcileGenerationRecoveryUsers(pending map[int64]bool,
	pendingAccounts map[int64]map[int64]bool, pendingTargets map[int64]map[string]bool,
	snapshot map[int64]uint64,
) {
	if r == nil {
		return
	}
	var replays []generationRecoveryReplay
	r.mu.Lock()
	r.ensureGenerationRecoveryMapsLocked()
	for userID := range pending {
		wasActive := r.generationRecoveryUsers[userID]
		if !wasActive {
			r.generationRecoveryEpoch[userID]++
		}
		r.activateGenerationRecoveryLocked(userID)
		expectedEpoch, observed := snapshot[userID]
		if pendingAccounts != nil && (!wasActive || (observed && expectedEpoch == r.generationRecoveryEpoch[userID])) {
			r.generationRecoveryAccounts[userID] = cloneGenerationRecoveryAccounts(pendingAccounts[userID])
			r.generationRecoveryTargets[userID] = cloneGenerationRecoveryTargets(pendingTargets[userID])
			r.generationRecoveryKnown[userID] = true
		}
	}
	for userID := range r.generationRecoveryUsers {
		expectedEpoch, observed := snapshot[userID]
		freshSnapshot := observed && expectedEpoch == r.generationRecoveryEpoch[userID]
		if pendingAccounts != nil && !pending[userID] && freshSnapshot {
			r.generationRecoveryAccounts[userID] = map[int64]bool{}
			r.generationRecoveryTargets[userID] = map[string]bool{}
			r.generationRecoveryKnown[userID] = true
		}
		if pending[userID] || r.generationRecoveryRuns[userID] || r.generationRecoveryReplay[userID] ||
			r.ordinaryMailboxSyncRunningLocked(userID) || r.attachmentDone[userID] != nil {
			continue
		}
		if !freshSnapshot {
			continue
		}
		replays = append(replays, r.clearGenerationRecoveryLocked(userID))
	}
	r.mu.Unlock()

	for _, replay := range replays {
		r.replayAfterGenerationRecovery(replay)
	}
}

func (r *Runner) refreshGenerationRecoveryGateForUser(ctx context.Context, userID int64) {
	if r == nil || r.Service == nil || r.Service.Store == nil || userID <= 0 {
		return
	}
	snapshot := r.generationRecoveryEpochSnapshotForUser(userID)
	if _, gated := snapshot[userID]; !gated {
		return
	}
	pending, err := r.Service.Store.HasPendingMailboxGenerationRebuildsForUser(ctx, userID)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("refresh mailbox generation recovery gate user_id=%d: %v", userID, err)
			r.wakeMailboxGenerationRebuildRecovery()
		}
		return
	}
	pendingUsers := map[int64]bool{}
	if pending {
		pendingUsers[userID] = true
	}
	r.reconcileGenerationRecoveryUsers(pendingUsers, nil, nil, snapshot)
	r.wakeMailboxGenerationRebuildRecovery()
}

func (r *Runner) activateGenerationRecoveryLocked(userID int64) {
	r.generationRecoveryUsers[userID] = true
	r.senderStatsPending[userID] = true
	r.attachmentPending[userID] = true
	r.pauseAttachmentIndexLocked(userID)
}

func (r *Runner) clearGenerationRecoveryLocked(userID int64) generationRecoveryReplay {
	replay := generationRecoveryReplay{
		userID:      userID,
		auto:        r.generationRecoveryAuto[userID],
		senderStats: r.senderStatsPending[userID],
		attachments: r.attachmentPending[userID],
	}
	for _, mailbox := range r.generationRecoveryBoxes[userID] {
		replay.mailboxes = append(replay.mailboxes, mailbox)
	}
	for _, request := range r.generationRecoveryAccts[userID] {
		replay.accountMailboxes = append(replay.accountMailboxes, request)
	}
	sortGenerationRecoveryReplay(&replay)
	delete(r.generationRecoveryUsers, userID)
	delete(r.generationRecoveryAccounts, userID)
	delete(r.generationRecoveryTargets, userID)
	delete(r.generationRecoveryKnown, userID)
	delete(r.generationRecoveryCursor, userID)
	delete(r.generationRecoveryAuto, userID)
	delete(r.generationRecoveryBoxes, userID)
	delete(r.generationRecoveryAccts, userID)
	r.generationRecoveryReplay[userID] = true
	return replay
}

func (r *Runner) replayAfterGenerationRecovery(replay generationRecoveryReplay) {
	if r.context().Err() != nil {
		return
	}
	replay = r.coalesceGenerationRecoveryReplay(replay)
	if r.replayGenerationRecovery != nil {
		r.replayGenerationRecovery(replay)
		r.mu.Lock()
		delete(r.generationRecoveryReplay, replay.userID)
		delete(r.senderStatsPending, replay.userID)
		delete(r.attachmentPending, replay.userID)
		r.mu.Unlock()
		return
	}
	go r.runGenerationRecoveryReplay(replay)
}

func (r *Runner) coalesceGenerationRecoveryReplay(replay generationRecoveryReplay) generationRecoveryReplay {
	covered := map[string]bool{}
	filteredMailboxes := make([]string, 0, len(replay.mailboxes))
	for _, mailbox := range uniqueMailboxes(replay.mailboxes) {
		key := strings.ToLower(strings.TrimSpace(mailbox))
		if covered[key] {
			continue
		}
		covered[key] = true
		filteredMailboxes = append(filteredMailboxes, mailbox)
	}
	replay.mailboxes = filteredMailboxes
	filteredAccounts := replay.accountMailboxes[:0]
	for _, request := range replay.accountMailboxes {
		if covered[strings.ToLower(strings.TrimSpace(request.mailbox))] {
			continue
		}
		filteredAccounts = append(filteredAccounts, request)
	}
	replay.accountMailboxes = filteredAccounts
	sortGenerationRecoveryReplay(&replay)
	return replay
}

func sortGenerationRecoveryReplay(replay *generationRecoveryReplay) {
	if replay == nil {
		return
	}
	sort.SliceStable(replay.mailboxes, func(i, j int) bool {
		return generationRecoveryMailboxLess(replay.mailboxes[i], replay.mailboxes[j])
	})
	sort.SliceStable(replay.accountMailboxes, func(i, j int) bool {
		left := replay.accountMailboxes[i]
		right := replay.accountMailboxes[j]
		leftInbox := strings.EqualFold(strings.TrimSpace(left.mailbox), "INBOX")
		rightInbox := strings.EqualFold(strings.TrimSpace(right.mailbox), "INBOX")
		if leftInbox != rightInbox {
			return leftInbox
		}
		if left.accountID != right.accountID {
			return left.accountID < right.accountID
		}
		return generationRecoveryMailboxLess(left.mailbox, right.mailbox)
	})
}

func generationRecoveryMailboxLess(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	leftInbox := strings.EqualFold(left, "INBOX")
	rightInbox := strings.EqualFold(right, "INBOX")
	if leftInbox != rightInbox {
		return leftInbox
	}
	leftLower := strings.ToLower(left)
	rightLower := strings.ToLower(right)
	if leftLower != rightLower {
		return leftLower < rightLower
	}
	return left < right
}

func (r *Runner) runGenerationRecoveryReplay(replay generationRecoveryReplay) {
	for r.context().Err() == nil {
		prepared, ok := r.prepareGenerationRecoveryReplay(replay)
		if !ok {
			r.pauseGenerationRecoveryReplay(replay)
			return
		}
		globalInboxCount := leadingGenerationRecoveryInboxes(prepared.mailboxes)
		for i, mailbox := range prepared.mailboxes[:globalInboxCount] {
			if !r.runGenerationRecoveryReplayMailboxes(prepared.userID, []string{mailbox}) {
				prepared.mailboxes = prepared.mailboxes[i:]
				r.pauseGenerationRecoveryReplay(prepared)
				return
			}
		}
		prepared.mailboxes = prepared.mailboxes[globalInboxCount:]
		accountInboxCount := leadingGenerationRecoveryAccountInboxes(prepared.accountMailboxes)
		for i, request := range prepared.accountMailboxes[:accountInboxCount] {
			if !r.runGenerationRecoveryReplayAccountMailbox(prepared.userID, request) {
				prepared.accountMailboxes = prepared.accountMailboxes[i:]
				r.pauseGenerationRecoveryReplay(prepared)
				return
			}
		}
		prepared.accountMailboxes = prepared.accountMailboxes[accountInboxCount:]
		for i, mailbox := range prepared.mailboxes {
			if !r.runGenerationRecoveryReplayMailboxes(prepared.userID, []string{mailbox}) {
				prepared.mailboxes = prepared.mailboxes[i:]
				r.pauseGenerationRecoveryReplay(prepared)
				return
			}
		}
		prepared.mailboxes = nil
		for i, request := range prepared.accountMailboxes {
			if !r.runGenerationRecoveryReplayAccountMailbox(prepared.userID, request) {
				prepared.accountMailboxes = prepared.accountMailboxes[i:]
				r.pauseGenerationRecoveryReplay(prepared)
				return
			}
		}
		prepared.accountMailboxes = nil

		pendingRecovery, err := r.Service.Store.HasPendingMailboxGenerationRebuildsForUser(r.context(), prepared.userID)
		if err != nil {
			if r.context().Err() != nil {
				return
			}
			log.Printf("finish mailbox generation replay user_id=%d: %v", prepared.userID, err)
			if waitForGenerationRecoveryReplay(r.context(), time.Second) != nil {
				return
			}
			replay = generationRecoveryReplay{userID: prepared.userID}
			continue
		}
		if pendingRecovery {
			r.SignalMailboxGenerationRecovery(prepared.userID)
			r.pauseGenerationRecoveryReplay(generationRecoveryReplay{userID: prepared.userID})
			return
		}

		r.mu.Lock()
		if r.generationRecoveryUsers[prepared.userID] {
			r.mu.Unlock()
			r.pauseGenerationRecoveryReplay(generationRecoveryReplay{userID: prepared.userID})
			return
		}
		next := r.takeDeferredGenerationRecoveryReplayLocked(prepared.userID)
		if next.auto || len(next.mailboxes) > 0 || len(next.accountMailboxes) > 0 {
			r.mu.Unlock()
			replay = next
			continue
		}
		if r.foregroundRunning[prepared.userID] > 0 {
			r.mu.Unlock()
			if waitForGenerationRecoveryReplay(r.context(), 10*time.Millisecond) != nil {
				return
			}
			replay = generationRecoveryReplay{userID: prepared.userID}
			continue
		}
		// Search indexing and sender statistics are derived maintenance. Release
		// the broad replay gate before running either one so a slow Bleve writer
		// cannot keep Inbox polling or remote IMAP copy routines paused.
		maintenance := generationRecoveryReplay{
			userID:      prepared.userID,
			senderStats: r.senderStatsPending[prepared.userID],
			attachments: r.attachmentPending[prepared.userID],
		}
		delete(r.generationRecoveryReplay, prepared.userID)
		r.mu.Unlock()
		log.Printf("recover mailbox generation replay complete user_id=%d sender_stats_queued=%t search_index_queued=%t",
			prepared.userID, maintenance.senderStats, maintenance.attachments)
		r.scheduleGenerationRecoveryWorkOutsideGate(maintenance)
		return
	}
}

func leadingGenerationRecoveryInboxes(mailboxes []string) int {
	count := 0
	for _, mailbox := range mailboxes {
		if !strings.EqualFold(strings.TrimSpace(mailbox), "INBOX") {
			break
		}
		count++
	}
	return count
}

func leadingGenerationRecoveryAccountInboxes(requests []deferredAccountMailbox) int {
	count := 0
	for _, request := range requests {
		if !strings.EqualFold(strings.TrimSpace(request.mailbox), "INBOX") {
			break
		}
		count++
	}
	return count
}

func (r *Runner) scheduleGenerationRecoveryWorkOutsideGate(replay generationRecoveryReplay) {
	replay = r.coalesceGenerationRecoveryReplay(replay)
	mailScheduled := false
	if replay.auto && r.Start(replay.userID) {
		mailScheduled = true
	}
	for _, mailbox := range replay.mailboxes {
		if r.StartPriorityMailboxes(replay.userID, []string{mailbox}) {
			mailScheduled = true
		}
	}
	for _, request := range replay.accountMailboxes {
		if r.QueueAccountMailboxes(replay.userID, request.accountID, []string{request.mailbox}) {
			mailScheduled = true
		}
	}
	if mailScheduled {
		return
	}
	if replay.senderStats {
		r.RefreshSenderStats(replay.userID)
	}
	if replay.attachments {
		r.StartAttachmentIndex(replay.userID)
	}
}

func (r *Runner) waitForGenerationRecoveryReplayMarkerCheck(userID int64) (bool, error) {
	for r.context().Err() == nil {
		pending, err := r.Service.Store.HasPendingMailboxGenerationRebuildsForUser(r.context(), userID)
		if err == nil {
			return pending, nil
		}
		log.Printf("check mailbox generation replay completion user_id=%d: %v", userID, err)
		r.wakeMailboxGenerationRebuildRecovery()
		if waitForGenerationRecoveryReplay(r.context(), time.Second) != nil {
			break
		}
	}
	return false, r.context().Err()
}

func (r *Runner) prepareGenerationRecoveryReplay(replay generationRecoveryReplay) (generationRecoveryReplay, bool) {
	if !replay.auto {
		return r.coalesceGenerationRecoveryReplay(replay), !r.generationRecoveryInterrupted(replay.userID)
	}
	for r.context().Err() == nil {
		if r.generationRecoveryInterrupted(replay.userID) {
			return replay, false
		}
		mailboxes, err := r.Service.AutoMailboxNames(r.context(), replay.userID)
		if err == nil {
			replay.auto = false
			replay.mailboxes = append(mailboxes, replay.mailboxes...)
			return r.coalesceGenerationRecoveryReplay(replay), true
		}
		log.Printf("plan mailbox generation replay user_id=%d: %v", replay.userID, err)
		if waitForGenerationRecoveryReplay(r.context(), time.Second) != nil {
			return replay, false
		}
	}
	return replay, false
}

func (r *Runner) runGenerationRecoveryReplayMailboxes(userID int64, mailboxes []string) bool {
	for r.context().Err() == nil {
		keys, reserved := r.reserveGenerationRecoveryReplayMailboxes(userID, mailboxes)
		if reserved {
			r.runReservedMailboxes(userID, mailboxes, keys)
			return !r.generationRecoveryInterrupted(userID)
		}
		if r.generationRecoveryInterrupted(userID) {
			return false
		}
		if waitForGenerationRecoveryReplay(r.context(), 10*time.Millisecond) != nil {
			return false
		}
	}
	return false
}

func (r *Runner) runGenerationRecoveryReplayAccountMailbox(userID int64, request deferredAccountMailbox) bool {
	mailboxes := []string{request.mailbox}
	for r.context().Err() == nil {
		keys, reserved := r.reserveGenerationRecoveryReplayAccountMailboxes(userID, request.accountID, mailboxes)
		if reserved {
			r.runReservedAccountMailboxes(userID, request.accountID, mailboxes, keys)
			return !r.generationRecoveryInterrupted(userID)
		}
		if r.generationRecoveryInterrupted(userID) {
			return false
		}
		if waitForGenerationRecoveryReplay(r.context(), 10*time.Millisecond) != nil {
			return false
		}
	}
	return false
}

func (r *Runner) reserveGenerationRecoveryReplayMailboxes(userID int64, mailboxes []string) ([]string, bool) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.generationRecoveryReplay[userID] || r.generationRecoveryUsers[userID] || r.foregroundRunning[userID] > 0 {
		return nil, false
	}
	for _, mailbox := range mailboxes {
		if r.mailboxReservedByAnyAccountLocked(userID, mailbox) {
			return nil, false
		}
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	return keys, true
}

func (r *Runner) reserveGenerationRecoveryReplayAccountMailboxes(userID, accountID int64, mailboxes []string) ([]string, bool) {
	keys := accountMailboxKeys(userID, accountID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.generationRecoveryReplay[userID] || r.generationRecoveryUsers[userID] || r.foregroundRunning[userID] > 0 {
		return nil, false
	}
	for _, mailbox := range mailboxes {
		if r.accountMailboxReservedLocked(userID, accountID, mailbox) {
			return nil, false
		}
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	return keys, true
}

func (r *Runner) generationRecoveryInterrupted(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generationRecoveryUsers[userID] || !r.generationRecoveryReplay[userID]
}

func (r *Runner) pauseGenerationRecoveryReplay(replay generationRecoveryReplay) {
	r.mu.Lock()
	if replay.auto {
		r.generationRecoveryAuto[replay.userID] = true
	}
	r.deferGenerationRecoveryMailboxesLocked(replay.userID, replay.mailboxes)
	for _, request := range replay.accountMailboxes {
		r.deferGenerationRecoveryAccountMailboxesLocked(replay.userID, request.accountID, []string{request.mailbox})
	}
	delete(r.generationRecoveryReplay, replay.userID)
	r.mu.Unlock()
	r.wakeMailboxGenerationRebuildRecovery()
}

func (r *Runner) takeDeferredGenerationRecoveryReplayLocked(userID int64) generationRecoveryReplay {
	replay := generationRecoveryReplay{userID: userID, auto: r.generationRecoveryAuto[userID]}
	for _, mailbox := range r.generationRecoveryBoxes[userID] {
		replay.mailboxes = append(replay.mailboxes, mailbox)
	}
	for _, request := range r.generationRecoveryAccts[userID] {
		replay.accountMailboxes = append(replay.accountMailboxes, request)
	}
	delete(r.generationRecoveryAuto, userID)
	delete(r.generationRecoveryBoxes, userID)
	delete(r.generationRecoveryAccts, userID)
	return replay
}

func waitForGenerationRecoveryReplay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Runner) ensureGenerationRecoveryMapsLocked() {
	if r.generationRecoveryUsers == nil {
		r.generationRecoveryUsers = map[int64]bool{}
	}
	if r.generationRecoveryRuns == nil {
		r.generationRecoveryRuns = map[int64]bool{}
	}
	if r.generationRecoveryReplay == nil {
		r.generationRecoveryReplay = map[int64]bool{}
	}
	if r.generationRecoveryEpoch == nil {
		r.generationRecoveryEpoch = map[int64]uint64{}
	}
	if r.generationRecoveryAccounts == nil {
		r.generationRecoveryAccounts = map[int64]map[int64]bool{}
	}
	if r.generationRecoveryTargets == nil {
		r.generationRecoveryTargets = map[int64]map[string]bool{}
	}
	if r.generationRecoveryKnown == nil {
		r.generationRecoveryKnown = map[int64]bool{}
	}
	if r.generationRecoveryCursor == nil {
		r.generationRecoveryCursor = map[int64]int64{}
	}
	if r.generationRecoveryActive == nil {
		r.generationRecoveryActive = map[int64]generationRecoveryActivity{}
	}
	if r.generationRecoveryAuto == nil {
		r.generationRecoveryAuto = map[int64]bool{}
	}
	if r.generationRecoveryBoxes == nil {
		r.generationRecoveryBoxes = map[int64]map[string]string{}
	}
	if r.generationRecoveryAccts == nil {
		r.generationRecoveryAccts = map[int64]map[string]deferredAccountMailbox{}
	}
	if r.senderStatsPending == nil {
		r.senderStatsPending = map[int64]bool{}
	}
	if r.attachmentPending == nil {
		r.attachmentPending = map[int64]bool{}
	}
}

func (r *Runner) generationRecoveryGatedLocked(userID int64) bool {
	return (r.generationRecoveryUsers != nil && r.generationRecoveryUsers[userID]) ||
		(r.generationRecoveryReplay != nil && r.generationRecoveryReplay[userID])
}

func (r *Runner) generationRecoveryAccountMailboxesGatedLocked(userID, accountID int64, mailboxes []string) bool {
	if r.generationRecoveryReplay[userID] && !r.generationRecoveryUsers[userID] && !r.generationRecoveryRuns[userID] {
		// Replay work also owns exact broad/account mailbox reservations. Keep
		// live Inbox polling available without allowing unrelated folder work to
		// fan out while the serialized replay drains.
		return !generationRecoveryInboxBypassAllowed(mailboxes) || r.generationRecoveryOrdinaryWriterRunningLocked(userID)
	}
	if r.generationRecoveryRuns[userID] {
		// Active work owns an exact mailbox reservation. Do not let an
		// unrelated Inbox poll wait behind a large recovery turn.
		if targets, known := r.generationRecoveryTargets[userID]; known {
			for _, mailbox := range mailboxes {
				if targets[accountMailboxKey(userID, accountID, mailbox)] {
					return true
				}
			}
			return !generationRecoveryInboxBypassAllowed(mailboxes) || r.generationRecoveryOrdinaryWriterRunningLocked(userID)
		}
		return true
	}
	if !r.generationRecoveryUsers[userID] {
		return false
	}
	if !r.generationRecoveryKnown[userID] {
		return true
	}
	if targets, known := r.generationRecoveryTargets[userID]; known {
		for _, mailbox := range mailboxes {
			if targets[accountMailboxKey(userID, accountID, mailbox)] {
				return true
			}
		}
		return !generationRecoveryInboxBypassAllowed(mailboxes) || r.generationRecoveryOrdinaryWriterRunningLocked(userID)
	}
	// Compatibility with a gate signaled before an exact target snapshot was
	// installed. The next durable scan replaces this account-wide fallback.
	return r.generationRecoveryAccounts[userID][accountID] || !generationRecoveryInboxBypassAllowed(mailboxes) ||
		r.generationRecoveryOrdinaryWriterRunningLocked(userID)
}

func generationRecoveryInboxBypassAllowed(mailboxes []string) bool {
	if len(mailboxes) == 0 {
		return false
	}
	for _, mailbox := range mailboxes {
		if !strings.EqualFold(strings.TrimSpace(mailbox), "INBOX") {
			return false
		}
	}
	return true
}

func cloneGenerationRecoveryAccounts(accounts map[int64]bool) map[int64]bool {
	out := make(map[int64]bool, len(accounts))
	for accountID := range accounts {
		if accountID > 0 {
			out[accountID] = true
		}
	}
	return out
}

func cloneGenerationRecoveryTargets(targets map[string]bool) map[string]bool {
	out := make(map[string]bool, len(targets))
	for target := range targets {
		if target = strings.TrimSpace(target); target != "" {
			out[target] = true
		}
	}
	return out
}

func (r *Runner) nextMailboxGenerationRecoveryAttempts(rebuilds []store.PendingMailboxGenerationRebuild) []store.PendingMailboxGenerationRebuild {
	firstByUserAccount := map[int64]map[int64]store.PendingMailboxGenerationRebuild{}
	accountsByUser := map[int64][]int64{}
	var userIDs []int64
	for _, rebuild := range rebuilds {
		if firstByUserAccount[rebuild.UserID] == nil {
			firstByUserAccount[rebuild.UserID] = map[int64]store.PendingMailboxGenerationRebuild{}
			userIDs = append(userIDs, rebuild.UserID)
		}
		if _, exists := firstByUserAccount[rebuild.UserID][rebuild.AccountID]; exists {
			continue
		}
		firstByUserAccount[rebuild.UserID][rebuild.AccountID] = rebuild
		accountsByUser[rebuild.UserID] = append(accountsByUser[rebuild.UserID], rebuild.AccountID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	attempts := make([]store.PendingMailboxGenerationRebuild, 0, len(userIDs))
	for _, userID := range userIDs {
		accountIDs := accountsByUser[userID]
		cursor := r.generationRecoveryCursor[userID]
		selected := accountIDs[0]
		for i, accountID := range accountIDs {
			if accountID == cursor {
				selected = accountIDs[(i+1)%len(accountIDs)]
				break
			}
		}
		attempts = append(attempts, firstByUserAccount[userID][selected])
	}
	return attempts
}

func (r *Runner) markMailboxGenerationRecoveryAttempt(rebuild store.PendingMailboxGenerationRebuild) {
	r.mu.Lock()
	r.ensureGenerationRecoveryMapsLocked()
	r.generationRecoveryCursor[rebuild.UserID] = rebuild.AccountID
	r.mu.Unlock()
}

func (r *Runner) deferGenerationRecoveryAutoLocked(userID int64) {
	r.ensureGenerationRecoveryMapsLocked()
	r.generationRecoveryAuto[userID] = true
}

func (r *Runner) deferGenerationRecoveryMailboxesLocked(userID int64, mailboxes []string) {
	r.ensureGenerationRecoveryMapsLocked()
	if r.generationRecoveryBoxes[userID] == nil {
		r.generationRecoveryBoxes[userID] = map[string]string{}
	}
	for _, mailbox := range uniqueMailboxes(mailboxes) {
		r.generationRecoveryBoxes[userID][strings.ToLower(mailbox)] = mailbox
	}
}

func (r *Runner) deferGenerationRecoveryAccountMailboxesLocked(userID, accountID int64, mailboxes []string) {
	r.ensureGenerationRecoveryMapsLocked()
	if r.generationRecoveryAccts[userID] == nil {
		r.generationRecoveryAccts[userID] = map[string]deferredAccountMailbox{}
	}
	for _, mailbox := range uniqueMailboxes(mailboxes) {
		key := accountMailboxKey(userID, accountID, mailbox)
		r.generationRecoveryAccts[userID][key] = deferredAccountMailbox{accountID: accountID, mailbox: mailbox}
	}
}

func (r *Runner) clearDeferredGenerationRecoveryAccountMailboxesLocked(userID, accountID int64, mailboxes []string) {
	requests := r.generationRecoveryAccts[userID]
	for _, mailbox := range uniqueMailboxes(mailboxes) {
		delete(requests, accountMailboxKey(userID, accountID, mailbox))
	}
	if len(requests) == 0 {
		delete(r.generationRecoveryAccts, userID)
	}
}

func (r *Runner) reserveGenerationRecoveryMailbox(rebuild store.PendingMailboxGenerationRebuild) ([]string, bool) {
	mailboxes := uniqueMailboxes([]string{rebuild.MailboxName})
	if rebuild.UserID <= 0 || rebuild.AccountID <= 0 || len(mailboxes) == 0 {
		return nil, false
	}
	keys := accountMailboxKeys(rebuild.UserID, rebuild.AccountID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureGenerationRecoveryMapsLocked()
	if !r.generationRecoveryUsers[rebuild.UserID] {
		r.generationRecoveryEpoch[rebuild.UserID]++
		r.activateGenerationRecoveryLocked(rebuild.UserID)
	}
	if r.generationRecoveryRuns[rebuild.UserID] || r.generationRecoveryReplay[rebuild.UserID] ||
		r.userWorkRunningLocked(rebuild.UserID) {
		return nil, false
	}
	r.generationRecoveryRuns[rebuild.UserID] = true
	startedAt := time.Now()
	r.generationRecoveryActive[rebuild.UserID] = generationRecoveryActivity{
		accountID:   rebuild.AccountID,
		mailbox:     rebuild.MailboxName,
		startedAt:   startedAt,
		diagnostics: newGenerationRecoveryDiagnostics(startedAt),
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	return keys, true
}

func (r *Runner) generationRecoveryDiagnosticsForUser(userID int64) *generationRecoveryDiagnostics {
	if r == nil || userID <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generationRecoveryActive[userID].diagnostics
}

func (r *Runner) releaseGenerationRecoveryMailbox(userID int64, keys []string) {
	r.mu.Lock()
	for _, key := range keys {
		delete(r.mailboxRunning, key)
	}
	delete(r.generationRecoveryRuns, userID)
	delete(r.generationRecoveryActive, userID)
	r.mu.Unlock()
}

func (r *Runner) userWorkRunningLocked(userID int64) bool {
	if r.autoRunning[userID] || r.foregroundRunning[userID] > 0 || r.senderStatsRunning[userID] {
		return true
	}
	prefix := mailboxKey(userID, "")
	for key := range r.mailboxRunning {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (r *Runner) ordinaryMailboxSyncRunningLocked(userID int64) bool {
	if r.autoRunning[userID] || r.foregroundRunning[userID] > 0 {
		return true
	}
	prefix := mailboxKey(userID, "")
	attachmentKey := mailboxKey(userID, "__attachments__")
	for key := range r.mailboxRunning {
		if key != attachmentKey && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (r *Runner) mailboxWriterRunningLocked(userID int64) bool {
	if r.generationRecoveryRuns[userID] || r.autoPlanning[userID] {
		return true
	}
	prefix := mailboxKey(userID, "")
	attachmentKey := mailboxKey(userID, "__attachments__")
	for key := range r.mailboxRunning {
		if key != attachmentKey && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (r *Runner) generationRecoveryOrdinaryWriterRunningLocked(userID int64) bool {
	if r.autoRunning[userID] || r.foregroundRunning[userID] > 0 {
		return true
	}
	prefix := mailboxKey(userID, "")
	attachmentKey := mailboxKey(userID, "__attachments__")
	activeKey := ""
	if activity, ok := r.generationRecoveryActive[userID]; ok {
		activeKey = accountMailboxKey(userID, activity.accountID, activity.mailbox)
	}
	for key := range r.mailboxRunning {
		if key == attachmentKey || key == activeKey || !strings.HasPrefix(key, prefix) {
			continue
		}
		return true
	}
	return false
}

func (r *Runner) generationRecoveryReplayWorkRunningLocked(userID int64) bool {
	prefix := mailboxKey(userID, "")
	for key := range r.mailboxRunning {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
