// File overview: Per-user trusted sender plugin for allowing remote message assets. migration declarations. Plugin schema migration declarations and helper persistence.

package sources

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"

	"rolltop/backend/plugins"
)

// Migrations returns schema changes for trusted remote-image senders.
func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		Scope:    plugins.ScopeUser,
		PluginID: plugins.TrustedImageSources,
		ID:       "001_create_sources",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS plugin_trusted_image_sources (
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				sender TEXT NOT NULL,
				created_at INTEGER NOT NULL,
				PRIMARY KEY(user_id, sender)
			)`,
		},
	}}
}

// TrustSender records that a user allowed remote images for a normalized sender.
func TrustSender(ctx context.Context, db *sql.DB, userID int64, sender string) error {
	sender = SenderIdentity(sender)
	if userID == 0 || sender == "" {
		return errors.New("trusted image source fields are incomplete")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_trusted_image_sources (user_id, sender, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id, sender) DO NOTHING`, userID, sender, nowUnix())
	return err
}

// IsSenderTrusted checks whether a user has allowed remote images for a normalized sender.
func IsSenderTrusted(ctx context.Context, db *sql.DB, userID int64, sender string) (bool, error) {
	sender = SenderIdentity(sender)
	if userID == 0 || sender == "" {
		return false, nil
	}
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM plugin_trusted_image_sources WHERE user_id = ? AND sender = ?`, userID, sender).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SenderIdentity normalizes sender text before it is used as a trust key.
func SenderIdentity(from string) string {
	from = strings.TrimSpace(from)
	if from == "" {
		return ""
	}
	if addrs, err := mail.ParseAddressList(from); err == nil && len(addrs) > 0 {
		return strings.ToLower(strings.TrimSpace(addrs[0].Address))
	}
	return strings.ToLower(strings.Trim(from, "<> \t\r\n"))
}

func nowUnix() int64 {
	return time.Now().UTC().Unix()
}
