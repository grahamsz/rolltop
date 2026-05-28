// File overview: Backend plugin hook dispatch during message sync.

package syncer

import (
	"context"

	"rolltop/backend/plugins"
)

func (s *Service) importIncomingMessageHooks(ctx context.Context, userID int64, raw []byte, parsedFrom string) error {
	backendPlugins, err := s.enabledBackendPlugins(ctx)
	if err != nil {
		return err
	}
	host := syncPluginHost{s: s}
	for _, backendPlugin := range backendPlugins {
		hook, ok := backendPlugin.(plugins.IncomingMessageHook)
		if !ok {
			continue
		}
		if err := hook.ImportIncomingMessage(ctx, host, userID, raw, parsedFrom); err != nil {
			return err
		}
	}
	return nil
}

type syncPluginHost struct {
	s *Service
}

func (h syncPluginHost) Store() any {
	return h.s.Store
}

func (h syncPluginHost) MasterKey() []byte {
	return nil
}

func (h syncPluginHost) PluginEnabled(ctx context.Context, pluginID string) bool {
	return h.s.pluginEnabled(ctx, pluginID)
}

var _ plugins.BackendHost = syncPluginHost{}
