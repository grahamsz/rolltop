package store

import (
	"context"
	"strings"
	"time"
)

func (s *Store) GetBIMIIcon(ctx context.Context, userID int64, domain string) (BIMIIcon, error) {
	var icon BIMIIcon
	var fetchedAt, expiresAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, domain, logo_url, svg, status, error, fetched_at, expires_at, updated_at
		FROM bimi_icons WHERE user_id = ? AND domain = ?`, userID, strings.ToLower(strings.TrimSpace(domain))).
		Scan(&icon.ID, &icon.UserID, &icon.Domain, &icon.LogoURL, &icon.SVG, &icon.Status, &icon.Error, &fetchedAt, &expiresAt, &updatedAt)
	if err != nil {
		return BIMIIcon{}, err
	}
	icon.FetchedAt = unixTime(fetchedAt)
	icon.ExpiresAt = unixTime(expiresAt)
	icon.UpdatedAt = unixTime(updatedAt)
	return icon, nil
}

func (s *Store) UpsertBIMIIcon(ctx context.Context, icon BIMIIcon) error {
	domain := strings.ToLower(strings.TrimSpace(icon.Domain))
	if icon.UserID == 0 || domain == "" {
		return nil
	}
	if icon.FetchedAt.IsZero() {
		icon.FetchedAt = timeNow()
	}
	if icon.UpdatedAt.IsZero() {
		icon.UpdatedAt = icon.FetchedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO bimi_icons
			(user_id, domain, logo_url, svg, status, error, fetched_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, domain) DO UPDATE SET
			logo_url = excluded.logo_url,
			svg = excluded.svg,
			status = excluded.status,
			error = excluded.error,
			fetched_at = excluded.fetched_at,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at`,
		icon.UserID, domain, icon.LogoURL, icon.SVG, icon.Status, icon.Error,
		icon.FetchedAt.UTC().Unix(), icon.ExpiresAt.UTC().Unix(), icon.UpdatedAt.UTC().Unix())
	return err
}

func timeNow() time.Time {
	return time.Unix(nowUnix(), 0).UTC()
}
