package main

import (
	"context"

	"rolltop/backend/plugins"
	"rolltop/plugins/client_side_pgp/backend/pgpmime"
)

func (p pgpBackend) ComposeMIMEBodyOverride(_ context.Context, _ plugins.BackendHost, _ int64, _ plugins.MailIdentityContext, body plugins.ComposeMessageBodyContext) (*plugins.MIMEBodyOverride, error) {
	return pgpmime.Override(body)
}
