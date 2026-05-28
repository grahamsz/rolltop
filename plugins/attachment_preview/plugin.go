package attachment_preview

import (
	"rolltop/backend/plugins"
	"rolltop/plugins/attachment_preview/preview"
)

func init() {
	plugins.Register(plugins.Definition{
		ID:          plugins.AttachmentPreview,
		Name:        "Attachment preview",
		Description: "Adds authenticated browser previews for supported image and PDF attachments.",
		Heavy:       true,
	}, preview.Migrations()...)
}
