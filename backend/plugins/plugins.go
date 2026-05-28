// File overview: Web-layer plugin integration helpers.

package plugins

import (
	"context"
	"database/sql"
	"strings"
)

const (
	BIMIBrandIcons       = "bimi_brand_icons"
	GravatarSenderIcons  = "gravatar_sender_icons"
	RemoteImageBlocklist = "remote_image_blocklist"
	TrustedImageSources  = "trusted_image_sources"
	AttachmentPreview    = "attachment_preview"
	LanguageSearch       = "language_search"
	OneClickUnsubscribe  = "one_click_unsubscribe"
	ClientSidePGP        = "client_side_pgp"
)

// Definition describes a compiled plugin and how it should appear in admin settings.
type Definition struct {
	ID               string
	Name             string
	Description      string
	EnabledByDefault bool
	Heavy            bool
}

// Migration describes one plugin-owned schema change and checksum source.
type Migration struct {
	PluginID   string
	ID         string
	Statements []string
	Apply      func(context.Context, *sql.Tx) error
}

// All returns every compiled plugin definition in display order for admin settings and migration seeding.
func All() []Definition {
	return []Definition{
		{
			ID:               BIMIBrandIcons,
			Name:             "BIMI brand icons",
			Description:      "Fetches and caches verified BIMI SVG logos for sender domains.",
			EnabledByDefault: true,
		},
		{
			ID:          GravatarSenderIcons,
			Name:        "Gravatar sender icons",
			Description: "Optionally proxies and caches Gravatar images for sender email addresses.",
		},
		{
			ID:               RemoteImageBlocklist,
			Name:             "Remote image blocklist",
			Description:      "Blocks remote tracking images and allows admin-maintained URL block patterns.",
			EnabledByDefault: true,
		},
		{
			ID:               TrustedImageSources,
			Name:             "Trusted image sources",
			Description:      "Remembers senders whose remote images may load automatically.",
			EnabledByDefault: true,
		},
		{
			ID:          AttachmentPreview,
			Name:        "Attachment preview",
			Description: "Adds authenticated browser previews for supported image and PDF attachments.",
			Heavy:       true,
		},
		{
			ID:               LanguageSearch,
			Name:             "Language search",
			Description:      "Detects message language during indexing and enables lang: search filters.",
			EnabledByDefault: true,
			Heavy:            true,
		},
		{
			ID:               OneClickUnsubscribe,
			Name:             "One-click unsubscribe",
			Description:      "Detects RFC 8058 unsubscribe links and sends one-click unsubscribe requests.",
			EnabledByDefault: true,
		},
		{
			ID:          ClientSidePGP,
			Name:        "Client-side PGP",
			Description: "Adds server-stored, passphrase-protected PGP keys with browser unlock, decrypt, sign, and encrypt controls.",
			Heavy:       true,
		},
	}
}

// Lookup returns one plugin definition by ID for enablement checks and plugin-specific routes.
func Lookup(id string) (Definition, bool) {
	id = strings.TrimSpace(id)
	for _, def := range All() {
		if def.ID == id {
			return def, true
		}
	}
	return Definition{}, false
}
