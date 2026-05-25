// File overview: Attachment preview plugin declaration.

package attachment_preview

import (
	"fmt"
	"io"
	"strings"

	"mailmirror/backend/plugins"
)

const MaxBytes = 25 * 1024 * 1024

type Attachment struct {
	ID          int64
	Filename    string
	ContentType string
}

type Preview struct {
	Available bool
	Kind      string
	URL       string
	Status    string
	PluginID  string
}

func Migrations() []plugins.Migration {
	return []plugins.Migration{{
		PluginID:   plugins.AttachmentPreview,
		ID:         "001_no_storage_required",
		Statements: []string{`SELECT 1`},
	}}
}

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

func CleanContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}

func SupportedImageType(contentType string) bool {
	switch CleanContentType(contentType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

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
