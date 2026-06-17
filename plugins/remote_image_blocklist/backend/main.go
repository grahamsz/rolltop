package main

import (
	"context"
	"database/sql"
	"regexp"

	"rolltop/backend/plugins"
	"rolltop/plugins/remote_image_blocklist/rules"
)

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return plugins.NoopBackendPlugin{PluginID: plugins.RemoteImageBlocklist}
}

type remoteImageBlocklistHook struct{}

func (remoteImageBlocklistHook) ListRemoteImageRules(ctx context.Context, db *sql.DB) ([]plugins.RemoteImageRule, error) {
	rows, err := rules.ListRules(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]plugins.RemoteImageRule, 0, len(rows))
	for _, row := range rows {
		out = append(out, plugins.RemoteImageRule{Pattern: row.Pattern, Enabled: row.Enabled})
	}
	return out, nil
}

func (remoteImageBlocklistHook) ListRemoteImagePatterns(ctx context.Context, db *sql.DB) ([]string, error) {
	return rules.ListPatterns(ctx, db)
}

func (remoteImageBlocklistHook) ReplaceRemoteImageRules(ctx context.Context, db *sql.DB, patterns []string) error {
	return rules.ReplaceRules(ctx, db, patterns)
}

func (remoteImageBlocklistHook) AllowRemoteImageFetch(ctx context.Context, db *sql.DB, req plugins.RemoteImageFetchRequest) (plugins.RemoteImageFetchDecision, error) {
	patterns, err := rules.ListPatterns(ctx, db)
	if err != nil {
		return plugins.RemoteImageFetchDecision{}, err
	}
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(req.URL) {
			return plugins.RemoteImageFetchDecision{Allow: false, Reason: "remote image blocklist"}, nil
		}
	}
	return plugins.RemoteImageFetchDecision{Allow: true}, nil
}
