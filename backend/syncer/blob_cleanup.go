// File overview: Durable, reference-safe cleanup for tenant blob metadata and files.

package syncer

import (
	"context"
	"errors"
	"log"

	"rolltop/backend/store"
)

const (
	genericBlobCleanupStartupLimit       = 1000
	genericBlobCleanupOpportunisticLimit = 25
)

// deleteUnreferencedBlob durably queues metadata first, then completes the
// filesystem/SQLite transition under the queue's writer-locked recheck.
func (s *Service) deleteUnreferencedBlob(ctx context.Context, userID, blobID int64, _ string) (bool, error) {
	entry, queued, err := s.Store.QueueBlobCleanupIfUnreferenced(ctx, userID, blobID)
	if err != nil || !queued {
		return false, err
	}
	// Some store-only maintenance/test services intentionally have no blob
	// backend. Leave the metadata and durable queue intact for a later runner
	// that can safely remove the tenant-owned file.
	if s.Blobs == nil {
		return false, nil
	}
	if err := s.Store.CompleteBlobCleanup(ctx, userID, entry.ID, s.blobCleanupDeleteCallback(userID)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) blobCleanupDeleteCallback(userID int64) func(string) error {
	return func(blobPath string) error {
		if s.Blobs == nil {
			return errors.New("blob store is not configured")
		}
		if !s.Blobs.OwnsUserBlobPath(userID, blobPath) {
			return errors.New("blob cleanup path is outside tenant scope")
		}
		return s.Blobs.DeleteUserBlob(userID, blobPath)
	}
}

func (s *Service) drainPendingBlobCleanupsForUser(ctx context.Context, userID int64, limit int) (completed, failed int, err error) {
	if s.Blobs == nil {
		return 0, 0, nil
	}
	entries, err := s.Store.ListBlobCleanupQueueForUser(ctx, userID, limit)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return completed, failed, err
		}
		err := s.Store.CompleteBlobCleanup(ctx, userID, entry.ID, s.blobCleanupDeleteCallback(userID))
		if err == nil || store.IsNotFound(err) {
			completed++
			continue
		}
		failed++
	}
	return completed, failed, nil
}

// DrainPendingBlobCleanups attempts a bounded batch for every tenant. Per-item
// filesystem failures remain journaled and do not prevent other users' cleanup.
func (s *Service) DrainPendingBlobCleanups(ctx context.Context, limitPerUser int) (completed, failed int, err error) {
	if s == nil || s.Store == nil {
		return 0, 0, errors.New("sync store is not configured")
	}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, user := range users {
		done, failedForUser, err := s.drainPendingBlobCleanupsForUser(ctx, user.ID, limitPerUser)
		completed += done
		failed += failedForUser
		if err != nil {
			return completed, failed, err
		}
	}
	return completed, failed, nil
}

// RecoverPendingBlobCleanups is invoked during server startup. Failures to
// remove individual paths are counted without logging path or message data.
func (r *Runner) RecoverPendingBlobCleanups() error {
	if r == nil || r.Service == nil || r.Service.Store == nil {
		return nil
	}
	completed, failed, err := r.Service.DrainPendingBlobCleanups(r.context(), genericBlobCleanupStartupLimit)
	if err != nil {
		return err
	}
	if failed > 0 {
		log.Printf("generic blob cleanup startup completed=%d failed=%d", completed, failed)
	}
	return nil
}
