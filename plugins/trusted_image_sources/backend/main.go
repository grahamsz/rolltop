package main

import (
	"context"
	"database/sql"

	"rolltop/backend/plugins"
	"rolltop/plugins/trusted_image_sources/sources"
)

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return plugins.NoopBackendPlugin{PluginID: plugins.TrustedImageSources}
}

type trustedImageSourcesHook struct{}

func (trustedImageSourcesHook) TrustImageSender(ctx context.Context, db *sql.DB, userID int64, sender string) error {
	return sources.TrustSender(ctx, db, userID, sender)
}

func (trustedImageSourcesHook) IsImageSenderTrusted(ctx context.Context, db *sql.DB, userID int64, sender string) (bool, error) {
	return sources.IsSenderTrusted(ctx, db, userID, sender)
}

func init() {
	plugins.Register(plugins.Definition{
		ID:               plugins.TrustedImageSources,
		Name:             "Trusted image sources",
		Description:      "Remembers senders whose remote images may load automatically.",
		EnabledByDefault: true,
	}, sources.Migrations()...)
	plugins.RegisterHooks(plugins.TrustedImageSources, trustedImageSourcesHook{})
}
