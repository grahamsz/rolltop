// File overview: Generic backend plugin hooks for message security detection
// and body transforms during sync/indexing.

package syncer

import (
	"context"
	"errors"

	"rolltop/backend/plugins"
)

func (s *Service) detectMessageSecurity(ctx context.Context, userID int64, raw []byte, body plugins.MessageBody) (plugins.MessageSecurityState, bool, error) {
	backendPlugins, err := s.enabledBackendPlugins(ctx)
	if err != nil {
		return plugins.MessageSecurityState{}, false, err
	}
	host := syncPluginHost{s: s}
	var out plugins.MessageSecurityState
	handled := false
	for _, backendPlugin := range backendPlugins {
		provider, ok := backendPlugin.(plugins.MessageSecurityProvider)
		if !ok {
			continue
		}
		generationRecoveryPhase(ctx, "plugin-security-detect", backendPlugin.ID())
		state, stateErr := provider.DetectMessageSecurity(ctx, host, userID, raw, body)
		if errors.Is(stateErr, plugins.ErrUnsupported) {
			continue
		}
		if stateErr != nil {
			return out, handled, stateErr
		}
		handled = true
		out.Encrypted = out.Encrypted || state.Encrypted
		out.Signed = out.Signed || state.Signed
	}
	return out, handled, nil
}

func (s *Service) transformMessageSecurityBody(ctx context.Context, userID int64, raw []byte, state plugins.MessageSecurityState, body plugins.MessageBody) (plugins.MessageBodyTransform, error) {
	backendPlugins, err := s.enabledBackendPlugins(ctx)
	if err != nil {
		return plugins.MessageBodyTransform{}, err
	}
	host := syncPluginHost{s: s}
	for _, backendPlugin := range backendPlugins {
		provider, ok := backendPlugin.(plugins.MessageSecurityProvider)
		if !ok {
			continue
		}
		generationRecoveryPhase(ctx, "plugin-security-transform", backendPlugin.ID())
		transform, transformErr := provider.TransformMessageBody(ctx, host, userID, raw, state, body)
		if errors.Is(transformErr, plugins.ErrUnsupported) {
			continue
		}
		if transformErr != nil {
			return plugins.MessageBodyTransform{}, transformErr
		}
		if transform.Applied {
			return transform, nil
		}
	}
	return plugins.MessageBodyTransform{}, nil
}
