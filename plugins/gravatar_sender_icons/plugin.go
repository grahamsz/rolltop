package gravatar_sender_icons

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/gravatar_sender_icons/gravatar"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:          plugins.GravatarSenderIcons,
		Name:        "Gravatar sender icons",
		Description: "Optionally proxies and caches Gravatar images for sender email addresses.",
	}, gravatar.Migrations()...)
}
