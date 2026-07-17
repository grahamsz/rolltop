// File overview: Sync-side cleanup after an atomic mailbox UIDVALIDITY reset.

package syncer

import (
	"context"
	"errors"

	"rolltop/backend/store"
)

// ArrivalUIDFloorAfterConfirmedUID returns the first UID that could have arrived
// after a confirmed APPEND. It refuses to wrap the IMAP UID space to zero.
func ArrivalUIDFloorAfterConfirmedUID(uid uint32) (uint32, error) {
	if uid == 0 {
		return 0, errors.New("confirmed APPEND UID is zero")
	}
	if uid == ^uint32(0) {
		return 0, errors.New("confirmed APPEND UID exhausted the UID space")
	}
	return uid + 1, nil
}

// ResetMailboxGenerationIfNeeded purges local rows and cached artifacts when a
// positive remote UIDVALIDITY cannot prove the current mailbox checkpoint and
// message rows belong to the same IMAP generation. arrivalUIDFloor must be the
// first UID that was not present when the remote generation was observed.
func (s *Service) ResetMailboxGenerationIfNeeded(ctx context.Context, userID int64, account store.MailAccount,
	mailbox store.Mailbox, remoteUIDValidity, arrivalUIDFloor uint32,
) (bool, error) {
	if remoteUIDValidity == 0 {
		return false, nil
	}
	_, reset, err := s.Store.ResetMailboxForRemoteGeneration(ctx, userID, account.ID, mailbox.ID,
		remoteUIDValidity, arrivalUIDFloor)
	if err != nil {
		return reset, err
	}
	if reset && s.MailboxGenerationRecoveryStarted != nil {
		s.MailboxGenerationRecoveryStarted(userID)
	}
	// A UIDVALIDITY reset makes every existing document for this mailbox stale.
	// Clearing that bounded mailbox scope prevents reused UIDs from surfacing old
	// mail. This is not an index audit: the recovered messages are indexed as
	// they are fetched, while any historical full audit remains explicit.
	if reset && s.Search != nil {
		if _, err := s.Search.PurgeMailbox(ctx, userID, mailbox.ID); err != nil {
			return true, err
		}
	}
	return reset, nil
}
