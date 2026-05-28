// File overview: Gravatar hash, cache record, and sender icon lookup helpers.

package gravatar_sender_icons

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"rolltop/backend/plugins"
)

const MaxImageBytes = 512 * 1024

// Image is the cached Gravatar result for one user/email hash pair.
type Image struct {
	ID          int64
	UserID      int64
	EmailHash   string
	ContentType string
	Image       []byte
	Status      string
	Error       string
	FetchedAt   time.Time
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

// Migrations returns schema changes for the Gravatar sender-icon cache.
func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		PluginID: plugins.GravatarSenderIcons,
		ID:       "001_create_cache",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS plugin_gravatar_cache (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				email_hash TEXT NOT NULL,
				content_type TEXT NOT NULL DEFAULT '',
				image BLOB,
				status TEXT NOT NULL DEFAULT '',
				error TEXT NOT NULL DEFAULT '',
				fetched_at INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL DEFAULT 0,
				updated_at INTEGER NOT NULL DEFAULT 0,
				UNIQUE(user_id, email_hash)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_gravatar_cache_user_hash ON plugin_gravatar_cache(user_id, email_hash)`,
		},
	}}
}

// Hash normalizes an email address and returns the MD5 digest required by Gravatar URLs.
func Hash(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	sum := md5.Sum([]byte(email))
	return hex.EncodeToString(sum[:])
}

// NormalizeHash validates and canonicalizes a stored Gravatar hash.
func NormalizeHash(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if i := strings.IndexByte(value, '/'); i >= 0 {
		value = value[:i]
	}
	value = strings.ToLower(value)
	if len(value) != 32 {
		return ""
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return value
}

// AssetURL builds the local URL where the frontend can request a cached Gravatar image.
func AssetURL(hash string) string {
	hash = NormalizeHash(hash)
	if hash == "" {
		return ""
	}
	return "/plugins/gravatar_sender_icons/avatar/" + hash
}

// FetchURL builds the remote Gravatar URL used by the refresh worker.
func FetchURL(hash string) string {
	hash = NormalizeHash(hash)
	if hash == "" {
		return ""
	}
	return "https://www.gravatar.com/avatar/" + hash + "?d=404&s=96"
}

// ErrorTTL returns the retry deadline after transient Gravatar failures.
func ErrorTTL(now time.Time) time.Time {
	return now.Add(12 * time.Hour)
}

// MissingTTL returns the retry deadline after Gravatar reports no image.
func MissingTTL(now time.Time) time.Time {
	return now.Add(7 * 24 * time.Hour)
}

// PositiveTTL returns the refresh deadline for a successfully cached Gravatar image.
func PositiveTTL(now time.Time) time.Time {
	return now.Add(30 * 24 * time.Hour)
}

// GetImage loads one cached Gravatar image row scoped by user and email hash.
func GetImage(ctx context.Context, db *sql.DB, userID int64, emailHash string) (Image, error) {
	var image Image
	var fetchedAt, expiresAt, updatedAt int64
	err := db.QueryRowContext(ctx, `SELECT id, user_id, email_hash, content_type, image, status, error, fetched_at, expires_at, updated_at
		FROM plugin_gravatar_cache WHERE user_id = ? AND email_hash = ?`, userID, NormalizeHash(emailHash)).
		Scan(&image.ID, &image.UserID, &image.EmailHash, &image.ContentType, &image.Image, &image.Status, &image.Error, &fetchedAt, &expiresAt, &updatedAt)
	if err != nil {
		return Image{}, err
	}
	image.FetchedAt = unixTime(fetchedAt)
	image.ExpiresAt = unixTime(expiresAt)
	image.UpdatedAt = unixTime(updatedAt)
	return image, nil
}

// UpsertImage records the latest Gravatar lookup result and local asset metadata.
func UpsertImage(ctx context.Context, db *sql.DB, image Image) error {
	emailHash := NormalizeHash(image.EmailHash)
	if image.UserID == 0 || emailHash == "" {
		return nil
	}
	if image.FetchedAt.IsZero() {
		image.FetchedAt = time.Now().UTC()
	}
	if image.UpdatedAt.IsZero() {
		image.UpdatedAt = image.FetchedAt
	}
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_gravatar_cache
			(user_id, email_hash, content_type, image, status, error, fetched_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, email_hash) DO UPDATE SET
			content_type = excluded.content_type,
			image = excluded.image,
			status = excluded.status,
			error = excluded.error,
			fetched_at = excluded.fetched_at,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at`,
		image.UserID, emailHash, image.ContentType, image.Image, image.Status, image.Error,
		image.FetchedAt.UTC().Unix(), image.ExpiresAt.UTC().Unix(), image.UpdatedAt.UTC().Unix())
	return err
}

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}
