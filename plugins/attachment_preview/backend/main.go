package main

import (
	"io"

	"rolltop/backend/plugins"
	"rolltop/plugins/attachment_preview/preview"
)

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return plugins.NoopBackendPlugin{PluginID: plugins.AttachmentPreview}
}

type attachmentPreviewHook struct{}

func (attachmentPreviewHook) PreviewForAttachment(att plugins.AttachmentPreviewInput) (plugins.AttachmentPreviewResult, bool) {
	result, ok := preview.ForAttachment(preview.Attachment{ID: att.ID, Filename: att.Filename, ContentType: att.ContentType})
	if !ok {
		return plugins.AttachmentPreviewResult{}, false
	}
	return plugins.AttachmentPreviewResult{
		Available: result.Available,
		Kind:      result.Kind,
		URL:       result.URL,
		Status:    result.Status,
		PluginID:  result.PluginID,
	}, true
}

func (attachmentPreviewHook) PreviewKind(att plugins.AttachmentPreviewInput) string {
	return preview.Kind(preview.Attachment{ID: att.ID, Filename: att.Filename, ContentType: att.ContentType})
}

func (attachmentPreviewHook) MaxPreviewBytes() int64 { return preview.MaxBytes }

func (attachmentPreviewHook) CleanPreviewContentType(contentType string) string {
	return preview.CleanContentType(contentType)
}

func (attachmentPreviewHook) SupportedPreviewImageType(contentType string) bool {
	return preview.SupportedImageType(contentType)
}

func (attachmentPreviewHook) PreviewImageTypeFromName(filename string) string {
	return preview.ImageTypeFromName(filename)
}

func (attachmentPreviewHook) ReadPreviewLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	return preview.ReadLimited(r, maxBytes)
}

func init() {
	plugins.Register(plugins.Definition{
		ID:          plugins.AttachmentPreview,
		Name:        "Attachment preview",
		Description: "Adds authenticated browser previews for supported image and PDF attachments.",
		Heavy:       true,
	}, preview.Migrations()...)
	plugins.RegisterHooks(plugins.AttachmentPreview, attachmentPreviewHook{})
}
