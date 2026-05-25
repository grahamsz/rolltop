package one_click_unsubscribe

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"

	"mailmirror/backend/plugins"
)

func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		PluginID: plugins.OneClickUnsubscribe,
		ID:       "001_create_sends",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS plugin_one_click_unsubscribe_sends (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
				sender TEXT NOT NULL DEFAULT '',
				unsubscribe_url TEXT NOT NULL,
				sent_at INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				UNIQUE(user_id, message_id, unsubscribe_url)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_one_click_unsubscribe_user_message ON plugin_one_click_unsubscribe_sends(user_id, message_id, sent_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_plugin_one_click_unsubscribe_user_url ON plugin_one_click_unsubscribe_sends(user_id, unsubscribe_url, sent_at DESC)`,
		},
	}}
}

type Send struct {
	ID             int64
	UserID         int64
	MessageID      int64
	Sender         string
	UnsubscribeURL string
	SentAt         time.Time
	CreatedAt      time.Time
}

func RecordSend(ctx context.Context, db *sql.DB, userID, messageID int64, sender, unsubscribeURL string, sentAt time.Time) error {
	sender = SenderIdentity(sender)
	unsubscribeURL = strings.TrimSpace(unsubscribeURL)
	if userID == 0 || messageID == 0 || unsubscribeURL == "" {
		return errors.New("unsubscribe send fields are incomplete")
	}
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	sentUnix := sentAt.UTC().Unix()
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_one_click_unsubscribe_sends
			(user_id, message_id, sender, unsubscribe_url, sent_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, message_id, unsubscribe_url) DO UPDATE SET
			sent_at = excluded.sent_at,
			created_at = excluded.created_at`,
		userID, messageID, sender, unsubscribeURL, sentUnix, sentUnix)
	return err
}

func LatestSend(ctx context.Context, db *sql.DB, userID, messageID int64, unsubscribeURL string, since time.Time) (Send, error) {
	unsubscribeURL = strings.TrimSpace(unsubscribeURL)
	if userID == 0 || (messageID == 0 && unsubscribeURL == "") {
		return Send{}, sql.ErrNoRows
	}
	sinceUnix := since.UTC().Unix()
	var send Send
	var sentAt, createdAt int64
	err := db.QueryRowContext(ctx, `SELECT id, user_id, message_id, sender, unsubscribe_url, sent_at, created_at
		FROM plugin_one_click_unsubscribe_sends
		WHERE user_id = ? AND sent_at >= ? AND (message_id = ? OR unsubscribe_url = ?)
		ORDER BY sent_at DESC LIMIT 1`,
		userID, sinceUnix, messageID, unsubscribeURL).
		Scan(&send.ID, &send.UserID, &send.MessageID, &send.Sender, &send.UnsubscribeURL, &sentAt, &createdAt)
	if err != nil {
		return Send{}, err
	}
	send.SentAt = unixTime(sentAt)
	send.CreatedAt = unixTime(createdAt)
	return send, nil
}

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

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}
