// File overview: Sync-side cleanup after an atomic mailbox UIDVALIDITY reset.

package syncer

import (
	"context"

	"rolltop/backend/store"
)

// ResetMailboxGenerationIfNeeded purges local rows and cached artifacts when a
// positive remote UIDVALIDITY cannot prove the current mailbox checkpoint and
// message rows belong to the same IMAP generation.
func (s *Service) ResetMailboxGenerationIfNeeded(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, remoteUIDValidity uint32) (bool, error) {
	if remoteUIDValidity == 0 {
		return false, nil
	}
	stale, reset, err := s.Store.ResetMailboxForRemoteUIDValidity(ctx, userID, account.ID, mailbox.ID, remoteUIDValidity)
	if err != nil {
		return reset, err
	}
	if reset {
		for _, msg := range stale {
			if s.Search != nil {
				if err := s.Search.DeleteMessage(ctx, msg.UserID, msg.ID); err != nil {
					return true, err
				}
			}
		}
	}
	if err := s.cleanupMailboxGenerationBlobs(ctx, userID, account.ID, mailbox.ID); err != nil {
		return reset, err
	}
	return reset, nil
}
