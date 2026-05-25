// File overview: Serialized sync runner that queues normal and priority sync jobs.

package syncer

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

// Runner serializes sync work per user/mailbox and launches background indexing follow-ups.
type Runner struct {
	Service *Service
	ctx     context.Context

	mu             sync.Mutex
	autoRunning    map[int64]bool
	mailboxRunning map[string]bool
	mailboxPending map[string]bool
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
	return &Runner{
		Service:        service,
		ctx:            ctx,
		autoRunning:    map[int64]bool{},
		mailboxRunning: map[string]bool{},
		mailboxPending: map[string]bool{},
	}
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
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.autoRunning, userID)
			r.mu.Unlock()
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
		r.StartAttachmentIndex(userID)
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
		r.StartAttachmentIndex(userID)
	}()
	return true
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
	keys, ok := r.reserveMailboxes(userID, mailboxes)
	if !ok {
		r.markPending(userID, mailboxes)
		return false
	}
	go func() {
		r.runReservedMailboxes(userID, mailboxes, keys)
		r.StartAttachmentIndex(userID)
	}()
	return true
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
			}
		}
		r.mu.Unlock()
		if len(rerun) > 0 && r.context().Err() == nil {
			r.StartPriorityMailboxes(userID, rerun)
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

func (r *Runner) runReservedAccountMailboxes(userID, accountID int64, mailboxes []string, keys []string) {
	defer func() {
		rerun := r.releaseAccountMailboxReservations(userID, mailboxes, keys)
		if len(rerun) > 0 && r.context().Err() == nil {
			r.StartPriorityMailboxes(userID, rerun)
		}
	}()
	ctx := r.context()
	if ctx.Err() != nil {
		return
	}
	if _, err := r.Service.SyncUserAccountMailboxes(ctx, userID, accountID, mailboxes); err != nil {
		log.Printf("sync user_id=%d account_id=%d mailboxes=%s: %v", userID, accountID, strings.Join(mailboxes, ","), err)
	}
}

func (r *Runner) releaseAccountMailboxReservations(userID int64, mailboxes []string, keys []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		delete(r.mailboxRunning, key)
	}
	var rerun []string
	seen := map[string]bool{}
	for _, mailbox := range mailboxes {
		key := mailboxKey(userID, mailbox)
		if !r.mailboxPending[key] {
			continue
		}
		delete(r.mailboxPending, key)
		name := strings.TrimSpace(mailbox)
		lower := strings.ToLower(name)
		if lower != "" && !seen[lower] {
			seen[lower] = true
			rerun = append(rerun, name)
		}
	}
	return rerun
}

// StartAttachmentIndex runs after message sync so newly fetched raw .eml data can
// be mined for attachment text and then discarded according to retention rules.
func (r *Runner) StartAttachmentIndex(userID int64) bool {
	if r.context().Err() != nil {
		return false
	}
	key := mailboxKey(userID, "__attachments__")
	r.mu.Lock()
	if r.mailboxRunning[key] {
		r.mu.Unlock()
		return false
	}
	r.mailboxRunning[key] = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.mailboxRunning, key)
			r.mu.Unlock()
		}()
		ctx := r.context()
		n, err := r.Service.IndexPendingAttachmentsForUser(ctx, userID, 100)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("attachment index user_id=%d: %v", userID, err)
			}
			return
		}
		if n > 0 {
			log.Printf("attachment index user_id=%d indexed=%d", userID, n)
		}
	}()
	return true
}

// IsRunning reports foreground sync activity for the user, excluding the private
// attachment-index sentinel so the chrome does not look stuck after mail fetches.
func (r *Runner) IsRunning(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoRunning[userID] {
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

func accountMailboxKeys(userID, accountID int64, mailboxes []string) []string {
	keys := make([]string, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		keys = append(keys, accountMailboxKey(userID, accountID, mailbox))
	}
	return keys
}
