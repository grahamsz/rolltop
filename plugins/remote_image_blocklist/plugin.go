package remote_image_blocklist

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/remote_image_blocklist/rules"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:               plugins.RemoteImageBlocklist,
		Name:             "Remote image blocklist",
		Description:      "Blocks remote tracking images and allows admin-maintained URL block patterns.",
		EnabledByDefault: true,
	}, rules.Migrations()...)
}
