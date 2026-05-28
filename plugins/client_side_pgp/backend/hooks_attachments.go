package main

import (
	"context"

	"rolltop/backend/plugins"
	"rolltop/plugins/client_side_pgp/backend/attachments"
)

func (p pgpBackend) AttachmentActions(_ context.Context, _ plugins.BackendHost, attachment plugins.AttachmentInfo) []plugins.AttachmentAction {
	return attachments.Actions(attachment)
}
