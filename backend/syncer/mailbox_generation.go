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
	// Search cleanup is derived work. Replacement documents are indexed after
	// bounded recovery releases its gate; stale generation documents wait for a
	// normal folder sync or an explicit offline search reset.
	return reset, nil
}
