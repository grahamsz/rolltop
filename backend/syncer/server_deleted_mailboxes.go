package syncer

import (
	"context"
	"errors"
	"log"
	"strings"

	"rolltop/backend/store"
)

const serverDeletedMailboxLabel = "Removing local mirror of server-deleted folder"

// Empty listings are not sufficient evidence for destructive reconciliation.
// A successful, nonempty LIST supplies candidates, not permission to bypass
// the runner's mailbox reservation or the later settings/LIST recheck.
func (s *Service) reconcileServerDeletedMailboxes(ctx context.Context, account store.MailAccount, infos []MailboxInfo) error {
	if !usableMailboxListing(infos) {
		return nil
	}
	local, err := s.Store.ListMailboxesForAccount(ctx, account.UserID, account.ID)
	if err != nil {
		return err
	}
	for _, mb := range local {
		eligible, err := s.serverDeletedMailboxEligible(ctx, account, mb, infos)
		if err != nil {
			return err
		}
		if !eligible {
			continue
		}
		if s.QueueServerDeletedMailbox != nil {
			err = s.QueueServerDeletedMailbox(account.UserID, mb)
		} else {
			// Standalone service callers have no scheduler, but still receive a
			// durable run and incremental progress, with local errors propagated.
			err = s.runServerDeletedMailboxCleanup(ctx, account.UserID, mb)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func usableMailboxListing(infos []MailboxInfo) bool {
	for _, info := range infos {
		if strings.TrimSpace(info.Name) != "" {
			return true
		}
	}
	return false
}

func (s *Service) serverDeletedMailboxEligible(ctx context.Context, account store.MailAccount, mb store.Mailbox, infos []MailboxInfo) (bool, error) {
	configured := strings.TrimSpace(account.Mailbox)
	if configured == "" {
		configured = store.DefaultMailboxPattern
	}
	if configured != "*" || !usableMailboxListing(infos) || mb.UserID != account.UserID || mb.AccountID != account.ID {
		return false, nil
	}
	if strings.EqualFold(strings.TrimSpace(mb.Name), "INBOX") || mb.Role != "" {
		return false, nil
	}
	for _, info := range infos {
		if strings.EqualFold(strings.TrimSpace(info.Name), strings.TrimSpace(mb.Name)) {
			return false, nil
		}
	}
	mode, err := s.Store.EffectiveMailboxSyncMode(ctx, account.UserID, account.ID, mb)
	return !strings.EqualFold(mode, "never"), err
}

func (r *Runner) queueServerDeletedMailbox(userID int64, mb store.Mailbox) error {
	_, _, err := r.StartMailboxMaintenance(userID, mb, serverDeletedMailboxLabel,
		func(ctx context.Context, runID int64, progress *store.SyncProgress) error {
			return r.Service.pruneServerDeletedMailbox(ctx, userID, mb, runID, progress)
		})
	// A busy mailbox is retried by the next discovery. No data is removed
	// until an account-qualified maintenance reservation has been acquired.
	return err
}

func (s *Service) runServerDeletedMailboxCleanup(ctx context.Context, userID int64, mb store.Mailbox) (resultErr error) {
	run, err := s.Store.CreateSyncRun(ctx, userID, mb.AccountID)
	if err != nil {
		return err
	}
	progress := store.SyncProgress{MailboxesTotal: 1, CurrentMailbox: mb.Name, LatestNewFrom: "rolltop:maintenance", LatestNewSubject: serverDeletedMailboxLabel}
	defer func() {
		status, message := "ok", ""
		if resultErr != nil {
			status, message = "failed", resultErr.Error()
			if ctx.Err() != nil {
				status = "interrupted"
			}
		} else {
			progress.MailboxesDone = 1
		}
		resultErr = errors.Join(resultErr, s.Store.FinishSyncRun(context.Background(), userID, run.ID, status, progress, message))
		s.notify(userID)
	}()
	if err := s.updateSyncProgress(ctx, userID, run.ID, progress); err != nil {
		return err
	}
	return s.pruneServerDeletedMailbox(ctx, userID, mb, run.ID, &progress)
}

func (s *Service) pruneServerDeletedMailbox(ctx context.Context, userID int64, candidate store.Mailbox, runID int64, progress *store.SyncProgress) error {
	account, err := s.Store.GetMailAccountForUser(ctx, userID, candidate.AccountID)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	infos, err := s.Fetcher.ListMailboxes(ctx, account)
	if err != nil {
		return err
	}
	// Network operations can be slow. Read local settings after LIST so a
	// user excluding a folder while the probe runs still prevents cleanup.
	mb, err := s.Store.GetMailboxForUser(ctx, userID, candidate.ID)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	account, err = s.Store.GetMailAccountForUser(ctx, userID, candidate.AccountID)
	if err != nil {
		return err
	}
	eligible, err := s.serverDeletedMailboxEligible(ctx, account, mb, infos)
	if err != nil || !eligible {
		return err
	}
	purged, err := s.PurgeMailboxLocalReferencesWithProgress(ctx, userID, mb.ID, runID, progress)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteMailbox(ctx, userID, mb.ID); err != nil {
		return err
	}
	log.Printf("pruned folder deleted on server user_id=%d account_id=%d mailbox=%q purged_messages=%d", userID, mb.AccountID, mb.Name, purged)
	return nil
}
