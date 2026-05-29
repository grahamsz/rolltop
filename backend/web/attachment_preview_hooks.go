// File overview: Web-layer dispatch for compiled attachment preview hooks.

package web

import "rolltop/backend/plugins"

func attachmentPreviewHook() (plugins.AttachmentPreviewHook, bool) {
	for _, hook := range plugins.Hooks(plugins.AttachmentPreview) {
		preview, ok := hook.(plugins.AttachmentPreviewHook)
		if ok {
			return preview, true
		}
	}
	return nil, false
}
