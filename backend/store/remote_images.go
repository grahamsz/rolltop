// File overview: User-scoped remote image cache metadata.

package store

import (
	"context"
	"time"
)

const (
	RemoteImageStatusOK      = "ok"
	RemoteImageStatusError   = "error"
	RemoteImageStatusBlocked = "blocked"
)

// GetRemoteImageCacheByHash loads one cached remote image row for a user/hash.
func (s *Store) GetRemoteImageCacheByHash(ctx context.Context, userID int64, urlHash string) (RemoteImageCache, error) {
	var item RemoteImageCache
	var fetched, expires, created, updated int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, url_hash, url, blob_id, blob_path, content_type, size, status, error, fetched_at, expires_at, created_at, updated_at
		FROM remote_image_cache WHERE user_id = ? AND url_hash = ?`, userID, urlHash).
		Scan(&item.ID, &item.UserID, &item.URLHash, &item.URL, &item.BlobID, &item.BlobPath, &item.ContentType, &item.Size, &item.Status, &item.Error, &fetched, &expires, &created, &updated)
	item.FetchedAt = unixTime(fetched)
	item.ExpiresAt = unixTime(expires)
	item.CreatedAt = unixTime(created)
	item.UpdatedAt = unixTime(updated)
	return item, err
}

// UpsertRemoteImageCache records the latest fetch/block/failure state for a URL.
func (s *Store) UpsertRemoteImageCache(ctx context.Context, item RemoteImageCache) (RemoteImageCache, error) {
	ts := nowUnix()
	if item.Status == "" {
		item.Status = RemoteImageStatusError
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = unixTime(ts)
	}
	_, err := s.mustDataDB(ctx, item.UserID).ExecContext(ctx, `INSERT INTO remote_image_cache
			(user_id, url_hash, url, blob_id, blob_path, content_type, size, status, error, fetched_at, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, url_hash) DO UPDATE SET
			url = excluded.url,
			blob_id = excluded.blob_id,
			blob_path = excluded.blob_path,
			content_type = excluded.content_type,
			size = excluded.size,
			status = excluded.status,
			error = excluded.error,
			fetched_at = excluded.fetched_at,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at`,
		item.UserID, item.URLHash, item.URL, item.BlobID, item.BlobPath, item.ContentType, item.Size, item.Status, item.Error,
		timeUnix(item.FetchedAt), timeUnix(item.ExpiresAt), timeUnix(item.CreatedAt), ts)
	if err != nil {
		return RemoteImageCache{}, err
	}
	return s.GetRemoteImageCacheByHash(ctx, item.UserID, item.URLHash)
}

// RemoteImageCacheFresh reports whether a cache row should suppress a fetch now.
func RemoteImageCacheFresh(item RemoteImageCache, nowUnix int64) bool {
	if item.Status == "" {
		return false
	}
	if item.ExpiresAt.IsZero() {
		return item.Status == RemoteImageStatusOK
	}
	return item.ExpiresAt.Unix() > nowUnix
}

func timeUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}
