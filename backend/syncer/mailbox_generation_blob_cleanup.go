// File overview: Sync-side draining of durable mailbox-generation blob cleanup entries.

package syncer

import (
	"context"
	"errors"
	"strings"

	"rolltop/backend/store"
)

func (s *Service) cleanupMailboxGenerationBlobs(ctx context.Context, userID, accountID, mailboxID int64) error {
	for {
		items, err := s.Store.ListMailboxGenerationBlobCleanup(ctx, userID, accountID, mailboxID, 100)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			var deletePath func(string) error
			if s.Blobs != nil {
				deletePath = func(path string) error {
					if strings.TrimSpace(path) == "" || !s.Blobs.OwnsUserBlobPath(userID, path) {
						return nil
					}
					return s.Blobs.DeleteUserBlob(userID, path)
				}
			}
			if err := s.Store.CompleteMailboxGenerationBlobCleanup(ctx, userID, item.ID, deletePath); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
	}
}
