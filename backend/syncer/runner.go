package syncer

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
)

type Runner struct {
	Service *Service
	ctx     context.Context

	mu             sync.Mutex
	autoRunning    map[int64]bool
	mailboxRunning map[string]bool
	mailboxPending map[string]bool
}

func NewRunner(service *Service) *Runner {
	return NewRunnerWithContext(context.Background(), service)
}

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

func (r *Runner) reserveMailboxes(userID int64, mailboxes []string) ([]string, bool) {
	keys := mailboxKeys(userID, mailboxes)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		if r.mailboxRunning[key] {
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
	for _, key := range keys {
		if r.mailboxRunning[key] {
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
		r.mu.Lock()
		for _, key := range keys {
			delete(r.mailboxRunning, key)
		}
		r.mu.Unlock()
	}()
	ctx := r.context()
	if ctx.Err() != nil {
		return
	}
	if _, err := r.Service.SyncUserAccountMailboxes(ctx, userID, accountID, mailboxes); err != nil {
		log.Printf("sync user_id=%d account_id=%d mailboxes=%s: %v", userID, accountID, strings.Join(mailboxes, ","), err)
	}
}

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

func (r *Runner) IsMailboxRunning(userID int64, mailbox string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxRunning[mailboxKey(userID, mailbox)]
}

func (r *Runner) IsAccountMailboxRunning(userID, accountID int64, mailbox string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxRunning[accountMailboxKey(userID, accountID, mailbox)] || r.mailboxRunning[mailboxKey(userID, mailbox)]
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
