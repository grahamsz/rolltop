// File overview: Durable, tenant-scoped cleanup queue for unreferenced blob metadata and files.

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BlobCleanupQueueEntry snapshots the metadata version that owned a path when
// it became unreferenced. Completion fails closed if that ownership changes.
type BlobCleanupQueueEntry struct {
	ID            int64
	UserID        int64
	BlobID        int64
	BlobPath      string
	BlobSHA256    string
	BlobSize      int64
	BlobCreatedAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// QueueBlobCleanupIfUnreferenced atomically records the current blob version
// only when no tenant-owned row references it. Blob metadata remains live until
// CompleteBlobCleanup commits after filesystem deletion.
func (s *Store) QueueBlobCleanupIfUnreferenced(ctx context.Context, userID, blobID int64) (BlobCleanupQueueEntry, bool, error) {
	if userID <= 0 || blobID <= 0 {
		return BlobCleanupQueueEntry{}, false, errors.New("invalid blob cleanup scope")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return BlobCleanupQueueEntry{}, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return BlobCleanupQueueEntry{}, false, err
	}
	defer tx.Rollback()
	now := nowUnix()
	result, err := tx.ExecContext(ctx, `INSERT INTO blob_cleanup_queue
		(user_id, blob_id, blob_path, blob_sha256, blob_size, blob_created_at, created_at, updated_at)
		SELECT blob.user_id, blob.id, blob.path, blob.sha256, blob.size, blob.created_at, ?, ?
		FROM blobs blob
		WHERE blob.user_id = ? AND blob.id = ?
		AND NOT EXISTS (SELECT 1 FROM messages
			WHERE messages.user_id = blob.user_id AND messages.blob_id = blob.id)
		AND NOT EXISTS (SELECT 1 FROM attachments
			WHERE attachments.user_id = blob.user_id AND attachments.blob_id = blob.id)
		AND NOT EXISTS (SELECT 1 FROM contact_icons
			WHERE contact_icons.user_id = blob.user_id AND contact_icons.blob_id = blob.id)
		AND NOT EXISTS (SELECT 1 FROM remote_image_cache
			WHERE remote_image_cache.user_id = blob.user_id AND remote_image_cache.blob_id = blob.id)
		ON CONFLICT(user_id, blob_id) DO UPDATE SET
			blob_path = excluded.blob_path,
			blob_sha256 = excluded.blob_sha256,
			blob_size = excluded.blob_size,
			blob_created_at = excluded.blob_created_at,
			updated_at = excluded.updated_at`, now, now, userID, blobID)
	if err != nil {
		return BlobCleanupQueueEntry{}, false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return BlobCleanupQueueEntry{}, false, err
	}
	if rowsAffected == 0 {
		if err := tx.Commit(); err != nil {
			return BlobCleanupQueueEntry{}, false, err
		}
		return BlobCleanupQueueEntry{}, false, nil
	}
	entry, err := scanBlobCleanupQueueEntry(tx.QueryRowContext(ctx, blobCleanupQueueSelect+
		`WHERE user_id = ? AND blob_id = ?`, userID, blobID))
	if err != nil {
		return BlobCleanupQueueEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BlobCleanupQueueEntry{}, false, err
	}
	return entry, true, nil
}

const blobCleanupQueueSelect = `SELECT id, user_id, blob_id, blob_path, blob_sha256,
	blob_size, blob_created_at, created_at, updated_at FROM blob_cleanup_queue `

// ListBlobCleanupQueueForUser returns a bounded tenant-owned cleanup batch.
func (s *Store) ListBlobCleanupQueueForUser(ctx context.Context, userID int64, limit int) ([]BlobCleanupQueueEntry, error) {
	if userID <= 0 {
		return nil, errors.New("invalid blob cleanup scope")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, blobCleanupQueueSelect+
		`WHERE user_id = ? ORDER BY id LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlobCleanupQueueEntry
	for rows.Next() {
		entry, err := scanBlobCleanupQueueEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

type blobCleanupScanDest interface {
	Scan(...any) error
}

func scanBlobCleanupQueueEntry(row blobCleanupScanDest) (BlobCleanupQueueEntry, error) {
	var entry BlobCleanupQueueEntry
	var blobCreatedAt, createdAt, updatedAt int64
	err := row.Scan(&entry.ID, &entry.UserID, &entry.BlobID, &entry.BlobPath,
		&entry.BlobSHA256, &entry.BlobSize, &blobCreatedAt, &createdAt, &updatedAt)
	entry.BlobCreatedAt = unixTime(blobCreatedAt)
	entry.CreatedAt = unixTime(createdAt)
	entry.UpdatedAt = unixTime(updatedAt)
	return entry, err
}

// CompleteBlobCleanup holds SQLite's writer lock while it rechecks references
// and path ownership, deletes the file, and removes metadata plus the journal.
// deletePath must not access this Store. Callback or commit failure leaves the
// SQLite metadata and queue entry available for an idempotent retry.
func (s *Store) CompleteBlobCleanup(ctx context.Context, userID, cleanupID int64, deletePath func(string) error) error {
	if userID <= 0 || cleanupID <= 0 {
		return errors.New("invalid blob cleanup entry")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE blob_cleanup_queue SET updated_at = ?
		WHERE user_id = ? AND id = ?`, nowUnix(), userID, cleanupID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected != 1 {
		return ErrNotFound
	}
	entry, err := scanBlobCleanupQueueEntry(tx.QueryRowContext(ctx, blobCleanupQueueSelect+
		`WHERE user_id = ? AND id = ?`, userID, cleanupID))
	if err != nil {
		return err
	}

	var currentPath, currentSHA string
	var currentSize, currentCreatedAt int64
	err = tx.QueryRowContext(ctx, `SELECT path, sha256, size, created_at FROM blobs
		WHERE user_id = ? AND id = ?`, userID, entry.BlobID).
		Scan(&currentPath, &currentSHA, &currentSize, &currentCreatedAt)
	switch {
	case err == nil:
		if currentPath != entry.BlobPath || currentSHA != entry.BlobSHA256 ||
			currentSize != entry.BlobSize || currentCreatedAt != entry.BlobCreatedAt.Unix() {
			return finishBlobCleanupTx(ctx, tx, userID, cleanupID)
		}
		referenced, err := blobReferencedTx(ctx, tx, userID, entry.BlobID)
		if err != nil {
			return err
		}
		if referenced {
			return finishBlobCleanupTx(ctx, tx, userID, cleanupID)
		}
		if entry.BlobPath != "" {
			if deletePath == nil {
				return errors.New("blob cleanup requires filesystem callback")
			}
			if err := deletePath(entry.BlobPath); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM blobs
			WHERE user_id = ? AND id = ? AND path = ? AND sha256 = ? AND size = ? AND created_at = ?`,
			userID, entry.BlobID, entry.BlobPath, entry.BlobSHA256, entry.BlobSize, entry.BlobCreatedAt.Unix())
		if err != nil {
			return err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected != 1 {
			return errors.New("blob metadata changed during cleanup")
		}
	case errors.Is(err, sql.ErrNoRows):
		var pathOwnerID int64
		pathErr := tx.QueryRowContext(ctx, `SELECT id FROM blobs
			WHERE user_id = ? AND path = ?`, userID, entry.BlobPath).Scan(&pathOwnerID)
		if pathErr == nil {
			return finishBlobCleanupTx(ctx, tx, userID, cleanupID)
		}
		if !errors.Is(pathErr, sql.ErrNoRows) {
			return pathErr
		}
		if entry.BlobPath != "" {
			if deletePath == nil {
				return errors.New("blob cleanup requires filesystem callback")
			}
			if err := deletePath(entry.BlobPath); err != nil {
				return err
			}
		}
	default:
		return err
	}
	return finishBlobCleanupTx(ctx, tx, userID, cleanupID)
}

func blobReferencedTx(ctx context.Context, tx *sql.Tx, userID, blobID int64) (bool, error) {
	var referenced int
	err := tx.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM messages WHERE user_id = ? AND blob_id = ?)
		OR EXISTS (SELECT 1 FROM attachments WHERE user_id = ? AND blob_id = ?)
		OR EXISTS (SELECT 1 FROM contact_icons WHERE user_id = ? AND blob_id = ?)
		OR EXISTS (SELECT 1 FROM remote_image_cache WHERE user_id = ? AND blob_id = ?)`,
		userID, blobID, userID, blobID, userID, blobID, userID, blobID).Scan(&referenced)
	return referenced != 0, err
}

func finishBlobCleanupTx(ctx context.Context, tx *sql.Tx, userID, cleanupID int64) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM blob_cleanup_queue
		WHERE user_id = ? AND id = ?`, userID, cleanupID)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected != 1 {
		return errors.New("blob cleanup entry changed during completion")
	}
	return tx.Commit()
}
