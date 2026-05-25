// File overview: Web-layer plugin integration helpers.

package syncer

import "context"

func (s *Service) pluginEnabled(ctx context.Context, pluginID string) bool {
	if s == nil || s.Store == nil {
		return false
	}
	enabled, err := s.Store.PluginEnabled(ctx, pluginID)
	return err == nil && enabled
}
