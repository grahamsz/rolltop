// File overview: Serialized sync runner that queues normal and priority sync jobs.

package syncer

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"rolltop/backend/store"
)

// Runner serializes sync work per user/mailbox and launches background indexing follow-ups.
type Runner struct {
	Service *Service
	ctx     context.Context

	mu                      sync.Mutex
	autoRunning             map[int64]bool
	mailboxRunning          map[string]bool
	mailboxPending          map[string]bool
	accountMailboxPending   map[string]bool
	foregroundRunning       map[int64]int
	attachmentPending       map[int64]bool
	attachmentCancels       map[int64]context.CancelFunc
	attachmentDone          map[int64]chan struct{}
	arrivalScheduler        *inboxArrivalScheduler
	rebuildRecoveryRunning  bool
	rebuildRecoveryInterval time.Duration
	queueRebuildMailbox     func(store.PendingMailboxGenerationRebuild)
}

// NewRunner builds a process-lifetime scheduler using a background context. The
// main package uses NewRunnerWithContext so shutdown can interrupt running jobs.
func NewRunner(service *Service) *Runner {
	return NewRunnerWithContext(context.Background(), service)
}

// NewRunnerWithContext wires cancellation into all future jobs. When startup or
// shutdown cancels ctx, new sync jobs are refused and active jobs report
// interruption through syncAccount's deferred finish.
func NewRunnerWithContext(ctx context.Context, service *Service) *Runner {
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &Runner{
		Service:               service,
		ctx:                   ctx,
		autoRunning:           map[int64]bool{},
		mailboxRunning:        map[string]bool{},
		mailboxPending:        map[string]bool{},
		accountMailboxPending: map[string]bool{},
		foregroundRunning:     map[int64]int{},
		attachmentPending:     map[int64]bool{},
		attachmentCancels:     map[int64]context.CancelFunc{},
		attachmentDone:        map[int64]chan struct{}{},
	}
	runner.arrivalScheduler = newInboxArrivalScheduler(ctx, runner.finalizePendingInboxArrivals, runner.notifyInboxArrivals)
	if service != nil {
		service.ScheduleInboxArrival = runner.ScheduleInboxArrival
	}
	return runner
}

func (r *Runner) context() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// Start begins an account-wide sync for one user if one is not already running.
// It plans folders first, then runs them serially as mailbox jobs so per-folder
// progress and priority reruns stay visible.
func (r *Runner) Start(userID int64) bool {
	ctx := r.context()
	if ctx.Err() != nil {
		return false
	}
	r.mu.Lock()
	if r.autoRunning[userID] {
		r.mu.Unlock()
		return false
	}
	r.autoRunning[userID] = true
	r.pauseAttachmentIndexLocked(userID)
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.autoRunning, userID)
			r.mu.Unlock()
			r.StartAttachmentIndex(userID)
		}()
		mailboxes, err := r.Service.AutoMailboxNames(ctx, userID)
		if err != nil {
			log.Printf("plan sync user_id=%d: %v", userID, err)
			return
		}
		// Account-wide sync is deliberately decomposed into mailbox jobs. That
		// keeps long archive backfills visible and allows foreground INBOX work
		// to proceed without waiting behind unrelated folders.
		for _, mailbox := range mailboxes {
			if ctx.Err() != nil {
				return
			}
			if !r.runMailboxes(userID, []string{mailbox}) {
				log.Printf("sync user_id=%d mailbox=%s skipped: already running", userID, mailbox)
			}
		}
		r.RefreshSenderStats(userID)
	}()
	return true
}

// StartMailboxes schedules named folders across all accounts for a user. It is
// used after operations that should refresh source/destination mailboxes.
func (r *Runner) StartMailboxes(userID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	mailboxes = uniqueMailboxes(mailboxes)
	if len(mailboxes) == 0 {
		return false
	}
	keys, ok := r.reserveMailboxes(userID, mailboxes)
	if !ok {
		return false
	}
	go func() {
		r.runReservedMailboxes(userID, mailboxes, keys)
		r.RefreshSenderStats(userID)
		r.StartAttachmentIndex(userID)
	}()
	return true
}

// StartAccountMailboxes reserves account-qualified folder keys so identical
// mailbox names on different IMAP servers do not block each other unnecessarily.
func (r *Runner) StartAccountMailboxes(userID, accountID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	mailboxes = uniqueMailboxes(mailboxes)
	if len(mailboxes) == 0 || accountID <= 0 {
		return false
	}
	keys, ok := r.reserveAccountMailboxes(userID, accountID, mailboxes)
	if !ok {
		return false
	}
	go func() {
		r.runReservedAccountMailboxes(userID, accountID, mailboxes, keys)
		r.RefreshSenderStats(userID)
		r.StartAttachmentIndex(userID)
	}()
	return true
}

// QueueAccountMailboxes guarantees one account-qualified sync pass. If the
// destination is already reserved, the active job launches one follow-up pass
// after releasing it. Backend plugins use this after IMAP APPEND so the new
// remote message is mirrored without syncing same-named folders on every
// account.
func (r *Runner) QueueAccountMailboxes(userID, accountID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	mailboxes = uniqueMailboxes(mailboxes)
	if len(mailboxes) == 0 || accountID <= 0 {
		return false
	}
	for _, mailbox := range mailboxes {
		names := []string{mailbox}
		keys, reserved := r.reserveOrQueueAccountMailboxes(userID, accountID, names)
		if !reserved {
			continue
		}
		go func(names []string, keys []string) {
			r.runReservedAccountMailboxes(userID, accountID, names, keys)
			r.RefreshSenderStats(userID)
			r.StartAttachmentIndex(userID)
		}(names, keys)
	}
	return true
}

// StartMailboxMaintenance reserves one account/mailbox and runs local maintenance in the background.
func (r *Runner) StartMailboxMaintenance(userID int64, mailbox store.Mailbox, label string, fn func(context.Context, int64, *store.SyncProgress) error) (store.SyncRun, bool, error) {
	ctx := r.context()
	if ctx.Err() != nil {
		return store.SyncRun{}, false, nil
	}
	if r.Service == nil || r.Service.Store == nil {
		return store.SyncRun{}, false, fmt.Errorf("sync service is not configured")
	}
	if fn == nil {
		return store.SyncRun{}, false, fmt.Errorf("maintenance function is required")
	}
	mailboxes := uniqueMailboxes([]string{mailbox.Name})
	if userID <= 0 || mailbox.AccountID <= 0 || len(mailboxes) == 0 {
		return store.SyncRun{}, false, fmt.Errorf("mailbox is required")
	}
	keys, ok := r.reserveAccountMailboxes(userID, mailbox.AccountID, mailboxes)
	if !ok {
		return store.SyncRun{}, false, nil
	}
	run, err := r.Service.Store.CreateSyncRun(context.Background(), userID, mailbox.AccountID)
	if err != nil {
		r.startAccountMailboxReruns(userID, mailbox.AccountID, r.releaseAccountMailboxReservations(userID, mailbox.AccountID, mailboxes, keys))
		r.StartAttachmentIndex(userID)
		return store.SyncRun{}, true, err
	}
	progress := store.SyncProgress{MailboxesTotal: 1, CurrentMailbox: mailbox.Name, LatestNewFrom: "rolltop:maintenance", LatestNewSubject: label}
	if err := r.Service.Store.UpdateSyncRunProgress(context.Background(), userID, run.ID, progress); err != nil {
		r.startAccountMailboxReruns(userID, mailbox.AccountID, r.releaseAccountMailboxReservations(userID, mailbox.AccountID, mailboxes, keys))
		r.StartAttachmentIndex(userID)
		return store.SyncRun{}, true, err
	}
	r.Service.notify(userID)
	go r.runReservedMailboxMaintenance(userID, mailbox.AccountID, mailboxes, keys, run.ID, progress, fn)
	return run, true, nil
}

// StartAccountMaintenance reserves all known folders for one account and runs a
// local maintenance task in the background. It is used for destructive local
// cache work such as deleting an IMAP account from Rolltop.
func (r *Runner) StartAccountMaintenance(userID int64, account store.MailAccount, mailboxes []store.Mailbox, label string, fn func(context.Context, int64, *store.SyncProgress) error) (store.SyncRun, bool, error) {
	ctx := r.context()
	if ctx.Err() != nil {
		return store.SyncRun{}, false, nil
	}
	if r.Service == nil || r.Service.Store == nil {
		return store.SyncRun{}, false, fmt.Errorf("sync service is not configured")
	}
	if fn == nil {
		return store.SyncRun{}, false, fmt.Errorf("maintenance function is required")
	}
	names := mailboxNamesForReservation(mailboxes)
	if len(names) == 0 {
		names = []string{"__account__"}
	}
	keys, ok := r.reserveAccountMailboxes(userID, account.ID, names)
	if !ok {
		return store.SyncRun{}, false, nil
	}
	run, err := r.Service.Store.CreateSyncRun(context.Background(), userID, account.ID)
	if err != nil {
		r.startAccountMailboxReruns(userID, account.ID, r.releaseAccountMailboxReservations(userID, account.ID, names, keys))
		r.StartAttachmentIndex(userID)
		return store.SyncRun{}, true, err
	}
	progress := store.SyncProgress{
		MailboxesTotal:   len(mailboxes),
		CurrentMailbox:   accountMaintenanceLabel(account),
		LatestNewFrom:    "rolltop:maintenance",
		LatestNewSubject: label,
	}
	if err := r.Service.Store.UpdateSyncRunProgress(context.Background(), userID, run.ID, progress); err != nil {
		r.startAccountMailboxReruns(userID, account.ID, r.releaseAccountMailboxReservations(userID, account.ID, names, keys))
		r.StartAttachmentIndex(userID)
		return store.SyncRun{}, true, err
	}
	r.Service.notify(userID)
	go r.runReservedMailboxMaintenance(userID, account.ID, names, keys, run.ID, progress, fn)
	return run, true, nil
}

func (r *Runner) runReservedMailboxMaintenance(userID, accountID int64, mailboxes []string, keys []string, runID int64, progress store.SyncProgress, fn func(context.Context, int64, *store.SyncProgress) error) {
	status := "ok"
	errText := ""
	defer func() {
		if r.context().Err() != nil && status == "ok" && progress.MailboxesDone < progress.MailboxesTotal {
			status = "interrupted"
			errText = "Server stopped before this maintenance task finished."
		}
		if status == "ok" {
			progress.MailboxesDone = progress.MailboxesTotal
			r.RefreshSenderStats(userID)
		}
		if err := r.Service.Store.FinishSyncRun(context.Background(), userID, runID, status, progress, errText); err != nil {
			log.Printf("finish mailbox maintenance user_id=%d run_id=%d: %v", userID, runID, err)
		}
		r.Service.notify(userID)
		r.startAccountMailboxReruns(userID, accountID, r.releaseAccountMailboxReservations(userID, accountID, mailboxes, keys))
		r.StartAttachmentIndex(userID)
	}()
	if err := fn(r.context(), runID, &progress); err != nil {
		status = "failed"
		errText = err.Error()
		log.Printf("mailbox maintenance user_id=%d mailboxes=%s: %v", userID, strings.Join(mailboxes, ","), err)
	}
}

func mailboxNamesForReservation(mailboxes []store.Mailbox) []string {
	names := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		names = append(names, mailbox.Name)
	}
	return uniqueMailboxes(names)
}

func accountMaintenanceLabel(account store.MailAccount) string {
	if strings.TrimSpace(account.Label) != "" {
		return account.Label
	}
	if strings.TrimSpace(account.Email) != "" {
		return account.Email
	}
	return account.Host
}

// StartPriorityMailboxes records a pending rerun when the folder is already busy.
// The active job will launch one follow-up pass after releasing its reservation.
func (r *Runner) StartPriorityMailboxes(userID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	mailboxes = uniqueMailboxes(mailboxes)
	if len(mailboxes) == 0 {
		return false
	}
	started := false
	for _, mailbox := range mailboxes {
		names := []string{mailbox}
		keys, reserved := r.reserveOrQueueMailboxes(userID, names)
		if !reserved {
			continue
		}
		started = true
		go func(names []string, keys []string) {
			r.runReservedMailboxes(userID, names, keys)
			r.RefreshSenderStats(userID)
			r.StartAttachmentIndex(userID)
		}(names, keys)
	}
	return started
}

// RefreshSenderStats rebuilds the precomputed best-match sender boosts after
// sync or local mailbox maintenance has changed message/read-state data.
func (r *Runner) RefreshSenderStats(userID int64) {
	if r == nil || r.Service == nil || r.Service.Store == nil || userID <= 0 {
		return
	}
	if err := r.Service.Store.RefreshReadSenderStatsForUser(context.Background(), userID); err != nil {
		log.Printf("refresh sender stats user_id=%d: %v", userID, err)
	}
}

func (r *Runner) runMailboxes(userID int64, mailboxes []string) bool {
	if r.context().Err() != nil {
		return false
	}
	keys, ok := r.reserveMailboxes(userID, mailboxes)
	if !ok {
		return false
	}
	r.runReservedMailboxes(userID, mailboxes, keys)
	return true
}

// reserveMailboxes is the concurrency gate for user-level folder jobs. It claims
// a broad user/mailbox key and also checks account-qualified keys, because this
// job will sync the requested mailbox name across every account for the user.
func (r *Runner) reserveMailboxes(userID int64, mailboxes []string) ([]string, bool) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, mailbox := range mailboxes {
		if r.mailboxReservedByAnyAccountLocked(userID, mailbox) {
			return nil, false
		}
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	r.pauseAttachmentIndexLocked(userID)
	return keys, true
}

func (r *Runner) reserveAccountMailboxes(userID, accountID int64, mailboxes []string) ([]string, bool) {
	keys := accountMailboxKeys(userID, accountID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, mailbox := range mailboxes {
		if r.accountMailboxReservedLocked(userID, accountID, mailbox) {
			return nil, false
		}
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	r.pauseAttachmentIndexLocked(userID)
	return keys, true
}

func (r *Runner) reserveOrQueueMailboxes(userID int64, mailboxes []string) ([]string, bool) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, mailbox := range mailboxes {
		if !r.mailboxReservedByAnyAccountLocked(userID, mailbox) {
			continue
		}
		for _, key := range keys {
			r.mailboxPending[key] = true
		}
		return nil, false
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	r.pauseAttachmentIndexLocked(userID)
	return keys, true
}

func (r *Runner) reserveOrQueueAccountMailboxes(userID, accountID int64, mailboxes []string) ([]string, bool) {
	keys := accountMailboxKeys(userID, accountID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, mailbox := range mailboxes {
		if !r.accountMailboxReservedLocked(userID, accountID, mailbox) {
			continue
		}
		if r.accountMailboxPending == nil {
			r.accountMailboxPending = map[string]bool{}
		}
		for _, key := range keys {
			r.accountMailboxPending[key] = true
		}
		return nil, false
	}
	for _, key := range keys {
		r.mailboxRunning[key] = true
	}
	r.pauseAttachmentIndexLocked(userID)
	return keys, true
}

func (r *Runner) markPending(userID int64, mailboxes []string) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		r.mailboxPending[key] = true
	}
}

// runReservedMailboxes performs the already-reserved sync and then checks for
// priority reruns that arrived while it was busy.
func (r *Runner) runReservedMailboxes(userID int64, mailboxes []string, keys []string) {
	defer func() {
		r.mu.Lock()
		var rerun []string
		accountReruns := map[int64][]string{}
		seen := map[string]bool{}
		for i, key := range keys {
			delete(r.mailboxRunning, key)
			if r.mailboxPending[key] {
				delete(r.mailboxPending, key)
				name := mailboxes[i]
				lower := strings.ToLower(strings.TrimSpace(name))
				if lower != "" && !seen[lower] {
					seen[lower] = true
					rerun = append(rerun, name)
				}
				r.takeAccountPendingForMailboxLocked(userID, name)
				continue
			}
			name := mailboxes[i]
			for accountID := range r.takeAccountPendingForMailboxLocked(userID, name) {
				accountReruns[accountID] = append(accountReruns[accountID], name)
			}
		}
		r.mu.Unlock()
		if len(rerun) > 0 && r.context().Err() == nil {
			r.StartPriorityMailboxes(userID, rerun)
		}
		for accountID, names := range accountReruns {
			if r.context().Err() != nil {
				break
			}
			r.QueueAccountMailboxes(userID, accountID, names)
		}
	}()
	ctx := r.context()
	if ctx.Err() != nil {
		return
	}
	if _, err := r.Service.SyncUserMailboxes(ctx, userID, mailboxes); err != nil {
		log.Printf("sync user_id=%d mailboxes=%s: %v", userID, strings.Join(mailboxes, ","), err)
	}
}

func (r *Runner) takeAccountPendingForMailboxLocked(userID int64, mailbox string) map[int64]bool {
	out := map[int64]bool{}
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	for key := range r.accountMailboxPending {
		pendingUserID, accountID, pendingMailbox, ok := parseAccountMailboxKey(key)
		if !ok || pendingUserID != userID || pendingMailbox != mailbox {
			continue
		}
		delete(r.accountMailboxPending, key)
		out[accountID] = true
	}
	return out
}

func (r *Runner) runReservedAccountMailboxes(userID, accountID int64, mailboxes []string, keys []string) {
	defer func() {
		r.startAccountMailboxReruns(userID, accountID, r.releaseAccountMailboxReservations(userID, accountID, mailboxes, keys))
	}()
	ctx := r.context()
	if ctx.Err() != nil {
		return
	}
	if _, err := r.Service.SyncUserAccountMailboxes(ctx, userID, accountID, mailboxes); err != nil {
		log.Printf("sync user_id=%d account_id=%d mailboxes=%s: %v", userID, accountID, strings.Join(mailboxes, ","), err)
	}
}

type accountMailboxReruns struct {
	global  []string
	account []string
}

func (r *Runner) releaseAccountMailboxReservations(userID, accountID int64, mailboxes []string, keys []string) accountMailboxReruns {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		delete(r.mailboxRunning, key)
	}
	var reruns accountMailboxReruns
	seenGlobal := map[string]bool{}
	seenAccount := map[string]bool{}
	for _, mailbox := range mailboxes {
		name := strings.TrimSpace(mailbox)
		lower := strings.ToLower(name)
		if lower == "" {
			continue
		}
		globalKey := mailboxKey(userID, name)
		accountKey := accountMailboxKey(userID, accountID, name)
		if r.mailboxPending[globalKey] {
			delete(r.mailboxPending, globalKey)
			delete(r.accountMailboxPending, accountKey)
			if !seenGlobal[lower] {
				seenGlobal[lower] = true
				reruns.global = append(reruns.global, name)
			}
			continue
		}
		if r.accountMailboxPending[accountKey] {
			delete(r.accountMailboxPending, accountKey)
			if !seenAccount[lower] {
				seenAccount[lower] = true
				reruns.account = append(reruns.account, name)
			}
		}
	}
	return reruns
}

func (r *Runner) startAccountMailboxReruns(userID, accountID int64, reruns accountMailboxReruns) {
	if r.context().Err() != nil {
		return
	}
	if len(reruns.global) > 0 {
		r.StartPriorityMailboxes(userID, reruns.global)
	}
	if len(reruns.account) > 0 {
		r.QueueAccountMailboxes(userID, accountID, reruns.account)
	}
}

const attachmentIndexBatchSize = 25

// BeginForegroundOperation pauses low-priority attachment indexing while a
// direct user operation, such as an IMAP move, changes local message ownership.
// The returned function is idempotent and resumes pending work when the user is
// otherwise idle.
func (r *Runner) BeginForegroundOperation(ctx context.Context, userID int64) (func(), error) {
	if r == nil || userID <= 0 {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	r.foregroundRunning[userID]++
	done := r.attachmentDone[userID]
	r.pauseAttachmentIndexLocked(userID)
	r.mu.Unlock()

	var once sync.Once
	finish := func() {
		once.Do(func() {
			r.mu.Lock()
			if r.foregroundRunning[userID] <= 1 {
				delete(r.foregroundRunning, userID)
			} else {
				r.foregroundRunning[userID]--
			}
			r.mu.Unlock()
			r.StartAttachmentIndex(userID)
		})
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			finish()
			return func() {}, ctx.Err()
		}
	}
	return finish, nil
}

// StartAttachmentIndex runs after message sync so newly fetched raw .eml data can
// be mined for attachment text and then discarded according to retention rules.
// It yields to mailbox jobs and explicit user operations, then drains small
// batches while the user remains idle.
func (r *Runner) StartAttachmentIndex(userID int64) bool {
	if r == nil || r.Service == nil || r.Service.Store == nil || userID <= 0 || r.context().Err() != nil {
		return false
	}
	key := mailboxKey(userID, "__attachments__")
	r.mu.Lock()
	if r.userForegroundRunningLocked(userID) || r.mailboxRunning[key] {
		r.attachmentPending[userID] = true
		r.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(r.context())
	done := make(chan struct{})
	r.mailboxRunning[key] = true
	r.attachmentCancels[userID] = cancel
	r.attachmentDone[userID] = done
	delete(r.attachmentPending, userID)
	r.mu.Unlock()

	go func() {
		drainMore := false
		defer func() {
			r.mu.Lock()
			delete(r.mailboxRunning, key)
			delete(r.attachmentCancels, userID)
			delete(r.attachmentDone, userID)
			if drainMore {
				r.attachmentPending[userID] = true
			}
			restart := r.attachmentPending[userID] && !r.userForegroundRunningLocked(userID)
			r.mu.Unlock()
			close(done)
			if restart {
				r.StartAttachmentIndex(userID)
			}
		}()
		n, err := r.Service.IndexPendingAttachmentsForUser(ctx, userID, attachmentIndexBatchSize)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("attachment index user_id=%d: %v", userID, err)
			}
			return
		}
		drainMore = n == attachmentIndexBatchSize
		if n > 0 {
			log.Printf("attachment index user_id=%d indexed=%d", userID, n)
		}
	}()
	return true
}

func (r *Runner) pauseAttachmentIndexLocked(userID int64) {
	if cancel := r.attachmentCancels[userID]; cancel != nil {
		r.attachmentPending[userID] = true
		cancel()
	}
}

func (r *Runner) userForegroundRunningLocked(userID int64) bool {
	if r.autoRunning[userID] || r.foregroundRunning[userID] > 0 {
		return true
	}
	prefix := fmt.Sprintf("%d:", userID)
	attachmentKey := mailboxKey(userID, "__attachments__")
	for key := range r.mailboxRunning {
		if key != attachmentKey && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// IsRunning reports foreground sync activity for the user, excluding the private
// attachment-index sentinel so the chrome does not look stuck after mail fetches.
func (r *Runner) IsRunning(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoRunning[userID] || r.foregroundRunning[userID] > 0 {
		return true
	}
	prefix := fmt.Sprintf("%d:", userID)
	for key := range r.mailboxRunning {
		if key == mailboxKey(userID, "__attachments__") {
			continue
		}
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// IsMailboxRunning reports whether any user-level or account-qualified mailbox
// sync reservation is active for this folder name.
func (r *Runner) IsMailboxRunning(userID int64, mailbox string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxReservedByAnyAccountLocked(userID, mailbox)
}

// IsAccountMailboxRunning reports whether a broad user-level reservation or the
// exact account/mailbox reservation is active.
func (r *Runner) IsAccountMailboxRunning(userID, accountID int64, mailbox string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accountMailboxReservedLocked(userID, accountID, mailbox)
}

func uniqueMailboxes(mailboxes []string) []string {
	out := make([]string, 0, len(mailboxes))
	seen := map[string]bool{}
	for _, mailbox := range mailboxes {
		name := strings.TrimSpace(mailbox)
		key := strings.ToLower(name)
		if name != "" && !seen[key] {
			seen[key] = true
			out = append(out, name)
		}
	}
	return out
}

func (r *Runner) mailboxReservedByAnyAccountLocked(userID int64, mailbox string) bool {
	for key := range r.mailboxRunning {
		if reservationKeyMatchesMailbox(key, userID, mailbox) {
			return true
		}
	}
	return false
}

func (r *Runner) accountMailboxReservedLocked(userID, accountID int64, mailbox string) bool {
	return r.mailboxRunning[mailboxKey(userID, mailbox)] || r.mailboxRunning[accountMailboxKey(userID, accountID, mailbox)]
}

func reservationKeyMatchesMailbox(key string, userID int64, mailbox string) bool {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailbox == "" {
		return false
	}
	if key == mailboxKey(userID, mailbox) {
		return true
	}
	prefix := fmt.Sprintf("%d:", userID)
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	rest := strings.TrimPrefix(key, prefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return false
	}
	if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
		return false
	}
	return parts[1] == mailbox
}

func mailboxKey(userID int64, mailbox string) string {
	return fmt.Sprintf("%d:%s", userID, strings.ToLower(strings.TrimSpace(mailbox)))
}

func mailboxKeys(userID int64, mailboxes []string) []string {
	keys := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		keys = append(keys, mailboxKey(userID, mailbox))
	}
	return keys
}

func accountMailboxKey(userID, accountID int64, mailbox string) string {
	return fmt.Sprintf("%d:%d:%s", userID, accountID, strings.ToLower(strings.TrimSpace(mailbox)))
}

func parseAccountMailboxKey(key string) (int64, int64, string, bool) {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return 0, 0, "", false
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return 0, 0, "", false
	}
	accountID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || accountID <= 0 {
		return 0, 0, "", false
	}
	return userID, accountID, strings.ToLower(strings.TrimSpace(parts[2])), true
}

func accountMailboxKeys(userID, accountID int64, mailboxes []string) []string {
	keys := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		keys = append(keys, accountMailboxKey(userID, accountID, mailbox))
	}
	return keys
}
