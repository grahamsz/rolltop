package main

import (
	"context"

	"rolltop/backend/plugins"
	identityhooks "rolltop/plugins/client_side_pgp/backend/identity"
)

func (p pgpBackend) ComposeIdentitySecurity(ctx context.Context, host plugins.BackendHost, userID int64, identity plugins.MailIdentityContext) (plugins.IdentitySecurityInfo, error) {
	return identityhooks.Security(ctx, storeFromHost(host), userID, identity)
}

func (p pgpBackend) ComposeIdentityAttachment(ctx context.Context, host plugins.BackendHost, userID int64, identity plugins.MailIdentityContext, purpose string) (plugins.Attachment, error) {
	return identityhooks.PublicKeyAttachment(ctx, storeFromHost(host), userID, identity, purpose)
}
