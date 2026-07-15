// File overview: Crash-gap replay for locally stored post-reset mailbox arrivals.

package syncer

import (
	"context"

	"rolltop/backend/store"
)

func (s *Service) replayStoredGenerationRebuildArrivals(ctx context.Context, userID int64,
	account store.MailAccount, mailbox store.Mailbox, targetUIDValidity, arrivalUIDFloor uint32,
	runID int64, progress *store.SyncProgress,
) error {
	if arrivalUIDFloor == 0 {
		return nil
	}
	messages, err := s.Store.ListMailboxGenerationArrivalCandidates(ctx, userID, account.ID,
		mailbox.ID, targetUIDValidity, arrivalUIDFloor)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := FetchedMessage{
			Mailbox:      mailbox.Name,
			UID:          msg.UID,
			UIDValidity:  targetUIDValidity,
			InternalDate: msg.InternalDate,
			Size:         msg.Size,
		}
		if mailboxReceivesNewMailNotifications(mailbox) {
			if err := s.recordInboxArrival(ctx, userID, runID, msg, item, progress); err != nil {
				return err
			}
			continue
		}
		if _, err := s.Store.CancelSnoozeForNewMessage(ctx, userID, msg); err != nil {
			return err
		}
	}
	return nil
}
