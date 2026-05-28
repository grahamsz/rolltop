// File overview: Sync reconciliation between local rows and remote IMAP mailbox state.

package syncer

import (
	"context"
	"fmt"
	"strings"

	"rolltop/backend/store"
)

// reconcileMailboxUIDs treats IMAP as the source of truth for membership in a
// folder. If a UID disappears remotely because it was deleted or moved out, the
// local message/search row disappears too, and the raw blob is removed when safe.
func (s *Service) reconcileMailboxUIDs(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox) error {
	uids, err := s.Fetcher.UIDs(ctx, account, mailbox.Name)
	if err != nil {
		return fmt.Errorf("reconcile mailbox %q UIDs: %w", mailbox.Name, err)
	}
	stale, err := s.Store.DeleteMessagesMissingUIDs(ctx, userID, account.ID, mailbox.ID, uids)
	if err != nil {
		return err
	}
	for _, msg := range stale {
		if s.Search != nil {
			if err := s.Search.DeleteMessage(ctx, msg.UserID, msg.ID); err != nil {
				return err
			}
		}
		if strings.TrimSpace(msg.BlobPath) != "" && s.Blobs != nil {
			if err := s.Blobs.DeleteUserBlob(userID, msg.BlobPath); err != nil {
				return err
			}
		}
		if err := s.Store.DeleteBlobForUser(ctx, userID, msg.BlobID); err != nil && !store.IsNotFound(err) {
			return err
		}
	}
	if len(stale) > 0 {
		s.notify(userID)
	}
	return nil
}
