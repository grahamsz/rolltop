package main

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/trusted_image_sources/sources"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:               plugins.TrustedImageSources,
		Name:             "Trusted image sources",
		Description:      "Remembers senders whose remote images may load automatically.",
		EnabledByDefault: true,
	}, sources.Migrations()...)
	plugins.RegisterHooks(plugins.TrustedImageSources, trustedImageSourcesHook{})
}
