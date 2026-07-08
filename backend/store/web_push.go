// File overview: User-scoped browser Web Push subscription persistence.

package store

import (
	"context"
	"errors"
	"strings"
)

// SaveWebPushSubscription upserts one Push API endpoint for the signed-in user.
func (s *Store) SaveWebPushSubscription(ctx context.Context, userID int64, sub WebPushSubscription) (WebPushSubscription, error) {
	endpoint := strings.TrimSpace(sub.Endpoint)
	p256dh := strings.TrimSpace(sub.P256DH)
	auth := strings.TrimSpace(sub.Auth)
	if userID <= 0 || endpoint == "" || p256dh == "" || auth == "" {
		return WebPushSubscription{}, errors.New("web push subscription fields are incomplete")
	}
	userAgent := trimLimit(strings.TrimSpace(sub.UserAgent), 500)
	ts := nowUnix()
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `INSERT INTO web_push_subscriptions
			(user_id, endpoint, p256dh, auth, user_agent, created_at, updated_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, endpoint) DO UPDATE SET
			p256dh = excluded.p256dh,
			auth = excluded.auth,
			user_agent = excluded.user_agent,
			updated_at = excluded.updated_at,
			last_seen_at = excluded.last_seen_at`,
		userID, endpoint, p256dh, auth, userAgent, ts, ts, ts)
	if err != nil {
		return WebPushSubscription{}, err
	}
	return s.getWebPushSubscription(ctx, userID, endpoint)
}

func (s *Store) getWebPushSubscription(ctx context.Context, userID int64, endpoint string) (WebPushSubscription, error) {
	return scanWebPushSubscription(s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, user_id, endpoint, p256dh, auth, user_agent, created_at, updated_at, last_seen_at
		FROM web_push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint))
}

// ListWebPushSubscriptions returns the user's registered browser push endpoints.
func (s *Store) ListWebPushSubscriptions(ctx context.Context, userID int64) ([]WebPushSubscription, error) {
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, user_id, endpoint, p256dh, auth, user_agent, created_at, updated_at, last_seen_at
		FROM web_push_subscriptions WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebPushSubscription
	for rows.Next() {
		sub, err := scanWebPushSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteWebPushSubscription removes one endpoint owned by the signed-in user.
func (s *Store) DeleteWebPushSubscription(ctx context.Context, userID int64, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if userID <= 0 || endpoint == "" {
		return nil
	}
	_, err := s.mustDataDB(ctx, userID).ExecContext(ctx, `DELETE FROM web_push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

func scanWebPushSubscription(row scanDest) (WebPushSubscription, error) {
	var sub WebPushSubscription
	var created, updated, lastSeen int64
	err := row.Scan(&sub.ID, &sub.UserID, &sub.Endpoint, &sub.P256DH, &sub.Auth, &sub.UserAgent, &created, &updated, &lastSeen)
	sub.CreatedAt = unixTime(created)
	sub.UpdatedAt = unixTime(updated)
	sub.LastSeenAt = unixTime(lastSeen)
	return sub, err
}
