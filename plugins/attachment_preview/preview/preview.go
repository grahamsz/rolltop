// File overview: Attachment preview plugin declaration.

package preview

import (
	"fmt"
	"io"
	"strings"

	"rolltop/backend/plugins"
)

const MaxBytes = 25 * 1024 * 1024

// Attachment is the minimal attachment metadata the preview plugin needs to classify a file.
type Attachment struct {
	ID          int64
	Filename    string
	ContentType string
}

// Preview describes a frontend-renderable attachment preview option.
type Preview struct {
	Available bool
	Kind      string
	URL       string
	Status    string
	PluginID  string
}

// Migrations returns plugin schema changes for attachment preview metadata.
func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		Scope:      plugins.ScopeUser,
		PluginID:   plugins.AttachmentPreview,
		ID:         "001_no_storage_required",
		Statements: []string{`SELECT 1`},
	}}
}

// ForAttachment returns the preview descriptor for an attachment when its type can be rendered in-browser.
func ForAttachment(att Attachment) (Preview, bool) {
	kind := Kind(att)
	if kind == "" {
		return Preview{}, false
	}
	return Preview{
		Available: true,
		Kind:      kind,
		URL:       fmt.Sprintf("/plugins/attachment_preview/attachments/%d/preview", att.ID),
		Status:    "available",
		PluginID:  plugins.AttachmentPreview,
	}, true
}

// Kind classifies an attachment into the frontend preview kind understood by the plugin slot.
func Kind(att Attachment) string {
	contentType := CleanContentType(att.ContentType)
	filename := strings.ToLower(strings.TrimSpace(att.Filename))
	switch {
	case contentType == "application/pdf" || strings.HasSuffix(filename, ".pdf"):
		return "pdf"
	case SupportedImageType(contentType):
		return "image"
	case contentType == "" && (strings.HasSuffix(filename, ".png") || strings.HasSuffix(filename, ".jpg") || strings.HasSuffix(filename, ".jpeg") || strings.HasSuffix(filename, ".gif") || strings.HasSuffix(filename, ".webp")):
		return "image"
	default:
		return ""
	}
}

// CleanContentType strips MIME parameters so type checks compare only the media type.
func CleanContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

// SupportedImageType reports whether an image MIME type is safe for direct preview rendering.
func SupportedImageType(contentType string) bool {
	switch CleanContentType(contentType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// ImageTypeFromName infers previewable image type from a filename when MIME metadata is missing.
func ImageTypeFromName(filename string) string {
	filename = strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasSuffix(filename, ".png"):
		return "image/png"
	case strings.HasSuffix(filename, ".jpg"), strings.HasSuffix(filename, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(filename, ".gif"):
		return "image/gif"
	case strings.HasSuffix(filename, ".webp"):
		return "image/webp"
	default:
		return ""
	}
}

// ReadLimited reads a preview body with a hard byte limit to avoid loading oversized attachments into memory.
func ReadLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = MaxBytes
	}
	limited := io.LimitReader(r, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("content exceeds limit")
	}
	return data, nil
}
