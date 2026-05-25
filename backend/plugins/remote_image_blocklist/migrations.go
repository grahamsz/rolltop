// File overview: Admin-editable remote image blocklist plugin and default tracker patterns. migration declarations. Plugin schema migration declarations and helper persistence.

package remote_image_blocklist

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"mailmirror/backend/plugins"
)

var DefaultPatterns = []string{
	`(?i)https?://[^"'\s>]*(klaviyo|klclick|trk\.klclick)[^"'\s>]*(/open|/track|/event|pixel)`,
	`(?i)https?://[^"'\s>]*(list-manage\.com|mailchimp\.com|mandrillapp\.com)[^"'\s>]*(/track|/open|/mctrack|/pixel)`,
	`(?i)https?://[^"'\s>]*/(?:open\.php|track/open|pixel|beacon|1x1|transparent)[^"'\s>]*`,
}

// Migrations returns schema changes for the editable remote-image blocklist.
func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		PluginID: plugins.RemoteImageBlocklist,
		ID:       "001_create_rules",
		Statements: []string{
			`CREATE TABLE IF NOT EXISTS plugin_remote_image_blocklist_rules (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				pattern TEXT NOT NULL UNIQUE,
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			)`,
		},
	}}
}

// Rule is one admin-editable regex used to block tracker-style remote image requests.
type Rule struct {
	ID        int64
	Pattern   string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SeedRules inserts default tracker-blocking regexes without overwriting admin edits.
func SeedRules(ctx context.Context, db *sql.DB) error {
	ts := nowUnix()
	for _, pattern := range DefaultPatterns {
		if _, err := db.ExecContext(ctx, `INSERT INTO plugin_remote_image_blocklist_rules (pattern, enabled, created_at, updated_at)
			VALUES (?, 1, ?, ?)
			ON CONFLICT(pattern) DO NOTHING`, pattern, ts, ts); err != nil {
			return err
		}
	}
	return nil
}

// ListRules returns the editable blocklist rows in evaluation order.
func ListRules(ctx context.Context, db *sql.DB) ([]Rule, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, pattern, enabled, created_at, updated_at FROM plugin_remote_image_blocklist_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var rule Rule
		var enabled int
		var created, updated int64
		if err := rows.Scan(&rule.ID, &rule.Pattern, &enabled, &created, &updated); err != nil {
			return nil, err
		}
		rule.Enabled = enabled != 0
		rule.CreatedAt = unixTime(created)
		rule.UpdatedAt = unixTime(updated)
		out = append(out, rule)
	}
	return out, rows.Err()
}

// ListPatterns returns only regex strings for display-time filtering.
func ListPatterns(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT pattern FROM plugin_remote_image_blocklist_rules WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return nil, err
		}
		if strings.TrimSpace(pattern) != "" {
			out = append(out, pattern)
		}
	}
	return out, rows.Err()
}

// ReplaceRules atomically replaces the admin-maintained remote-image blocklist.
func ReplaceRules(ctx context.Context, db *sql.DB, patterns []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_remote_image_blocklist_rules`); err != nil {
		return err
	}
	ts := nowUnix()
	seen := map[string]bool{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || seen[pattern] {
			continue
		}
		seen[pattern] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_remote_image_blocklist_rules (pattern, enabled, created_at, updated_at) VALUES (?, 1, ?, ?)`, pattern, ts, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nowUnix() int64 {
	return time.Now().UTC().Unix()
}

func unixTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}
