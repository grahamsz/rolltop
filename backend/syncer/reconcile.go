// File overview: Sync reconciliation between local rows and remote IMAP mailbox state.

package syncer

import (
	"context"
	"fmt"

	"rolltop/backend/store"
)

// reconcileMailboxUIDs treats IMAP as the source of truth for membership in a
// folder. If a UID disappears remotely because it was deleted or moved out, the
// local message/search row disappears too, and the raw blob is removed when safe.
func (s *Service) reconcileMailboxUIDs(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox) error {
	var (
		stale []store.MessageRecord
		err   error
	)
	if snapshotFetcher, ok := s.Fetcher.(MailboxUIDSnapshotFetcher); ok {
		snapshot, snapshotErr := snapshotFetcher.SnapshotMailboxUIDs(ctx, account, mailbox.Name)
		if snapshotErr != nil {
			return fmt.Errorf("reconcile mailbox %q UID snapshot: %w", mailbox.Name, snapshotErr)
		}
		stale, err = s.Store.DeleteMessagesMissingUIDsAndRecordExpunges(ctx, userID, account.ID,
			mailbox.ID, snapshot.UIDs, snapshot.UIDValidity, snapshot.UIDNext, nil)
	} else {
		// Legacy fetchers cannot bind UIDs to a selected UIDVALIDITY. Preserve
		// local mirror cleanup, but never create evidence that could suppress a
		// later Inbox delivery notification.
		uids, uidErr := s.Fetcher.UIDs(ctx, account, mailbox.Name)
		if uidErr != nil {
			return fmt.Errorf("reconcile mailbox %q UIDs: %w", mailbox.Name, uidErr)
		}
		stale, err = s.Store.DeleteMessagesMissingUIDs(ctx, userID, account.ID, mailbox.ID, uids)
	}
	if err != nil {
		return err
	}
	for _, msg := range stale {
		if s.Search != nil {
			if err := s.Search.DeleteMessage(ctx, msg.UserID, msg.ID); err != nil {
				return err
			}
		}
		if _, err := s.deleteUnreferencedBlob(ctx, userID, msg.BlobID, msg.BlobPath); err != nil {
			return err
		}
	}
	if len(stale) > 0 {
		s.notify(userID)
	}
	return nil
}
