package main

import (
	"context"

	"rolltop/backend/plugins"
	autocrypthooks "rolltop/plugins/client_side_pgp/backend/autocrypt"
)

func (p pgpBackend) OutboundMailHeaders(ctx context.Context, host plugins.BackendHost, userID int64, identity plugins.MailIdentityContext) ([]plugins.MailHeader, error) {
	return autocrypthooks.OutboundMailHeaders(ctx, storeFromHost(host), userID, identity)
}

func (p pgpBackend) ImportIncomingMessage(ctx context.Context, host plugins.BackendHost, userID int64, raw []byte, parsedFrom string) error {
	return autocrypthooks.ImportIncomingMessage(ctx, storeFromHost(host), userID, raw, parsedFrom)
}
