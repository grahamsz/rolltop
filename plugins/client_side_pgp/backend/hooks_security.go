package main

import (
	"context"

	"rolltop/backend/plugins"
	securityhooks "rolltop/plugins/client_side_pgp/backend/security"
)

func (p pgpBackend) DetectMessageSecurity(_ context.Context, _ plugins.BackendHost, _ int64, raw []byte, body plugins.MessageBody) (plugins.MessageSecurityState, error) {
	return securityhooks.Detect(raw, body), nil
}

func (p pgpBackend) TransformMessageBody(_ context.Context, _ plugins.BackendHost, _ int64, raw []byte, state plugins.MessageSecurityState, body plugins.MessageBody) (plugins.MessageBodyTransform, error) {
	return securityhooks.Transform(raw, state, body), nil
}
