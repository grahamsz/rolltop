package attachments

import (
	"strings"

	"rolltop/backend/plugins"
)

func Actions(attachment plugins.AttachmentInfo) []plugins.AttachmentAction {
	if !isImportablePublicKeyAttachment(attachment) {
		return nil
	}
	return []plugins.AttachmentAction{{
		Kind:  "pgp-public-key-import",
		Label: "Import OpenPGP key",
	}}
}

func isImportablePublicKeyAttachment(attachment plugins.AttachmentInfo) bool {
	if attachment.Inline || attachment.Size <= 0 || attachment.Size > 16*1024 {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(attachment.Filename))
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	if strings.Contains(name, "signature") || strings.Contains(contentType, "pgp-signature") {
		return false
	}
	if !strings.HasSuffix(name, ".asc") {
		return false
	}
	return contentType == "" || strings.Contains(contentType, "pgp-keys") || strings.Contains(contentType, "text/plain")
}
