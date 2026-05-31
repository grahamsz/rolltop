// File overview: Host-side runtime backend plugin loading and API dispatch.

package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"rolltop/backend/plugins"
	"rolltop/backend/search"
)

func (s *Server) Store() any {
	if s == nil {
		return nil
	}
	return s.store
}

func (s *Server) MasterKey() []byte {
	if s == nil {
		return nil
	}
	return s.masterKey
}

func (s *Server) PluginEnabled(ctx context.Context, pluginID string) bool {
	return s.pluginEnabled(ctx, pluginID)
}

func (s *Server) RequireAPIAuth(w http.ResponseWriter, r *http.Request) (plugins.CurrentUser, bool) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return plugins.CurrentUser{}, false
	}
	return plugins.CurrentUser{UserID: cu.User.ID}, true
}

func (s *Server) VerifyCSRF(w http.ResponseWriter, r *http.Request) bool {
	return s.verifyCSRF(w, r)
}

func (s *Server) DecodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	return decodeJSON(w, r, dest)
}

func (s *Server) WriteJSON(w http.ResponseWriter, value any) {
	writeJSON(w, value)
}

func (s *Server) WriteAPIError(w http.ResponseWriter, status int, message string) {
	writeAPIError(w, status, message)
}

func (s *Server) ServerError(w http.ResponseWriter, err error) {
	s.serverError(w, err)
}

func (s *Server) MatchMessageSearch(ctx context.Context, userID, messageID int64, query string) (plugins.SearchMatchResult, error) {
	if s == nil || s.search == nil {
		return plugins.SearchMatchResult{}, errors.New("search is not configured")
	}
	hit, ok, err := s.search.MatchMessageWithOptions(ctx, userID, messageID, query, search.SearchOptions{})
	if err != nil {
		return plugins.SearchMatchResult{}, err
	}
	return plugins.SearchMatchResult{
		Matched:    ok,
		Score:      hit.Score,
		Terms:      hit.Terms,
		QueryTerms: hit.QueryTerms,
		Fields:     hit.Fields,
	}, nil
}

func (s *Server) StarMessage(ctx context.Context, userID, messageID int64, starred bool) error {
	if s == nil || s.syncer == nil {
		return errors.New("sync service is not configured")
	}
	msg, err := s.syncer.SetStarredForMessage(ctx, userID, messageID, starred)
	if err != nil {
		return err
	}
	if err := s.syncer.SyncStarStateForMessage(ctx, userID, msg.ID); err != nil {
		return err
	}
	s.notifyUserChanged(userID)
	return nil
}

func (s *Server) MoveMessage(ctx context.Context, userID, messageID, destMailboxID int64) error {
	if s == nil || s.syncer == nil {
		return errors.New("sync service is not configured")
	}
	if err := s.syncer.MoveMessage(ctx, userID, messageID, destMailboxID); err != nil {
		return err
	}
	s.notifyUserChanged(userID)
	return nil
}

func (s *Server) ForwardMessage(ctx context.Context, userID, messageID int64, to string, headers []plugins.MailHeader) error {
	if s == nil || s.syncer == nil {
		return errors.New("sync service is not configured")
	}
	if s.syncer.Sender == nil {
		s.syncer.Sender = s.sender
	}
	if len(s.syncer.MasterKey) == 0 {
		s.syncer.MasterKey = s.masterKey
	}
	return s.syncer.ForwardMessage(ctx, userID, messageID, to, headers)
}

func (s *Server) RegisterProtectedAPI(pluginID string, route plugins.ProtectedAPIRoute) (plugins.ProtectedAPIRouteHandle, error) {
	handle, err := s.protectedAPIRouteRegistry().register(pluginID, route)
	if err != nil {
		return nil, err
	}
	log.Printf("debug plugin protected api registered plugin_id=%s path=%s prefix=%t", strings.TrimSpace(pluginID), cleanAPIPath(route.Path), route.Prefix)
	return handle, nil
}

func (s *Server) apiBackendPlugin(w http.ResponseWriter, r *http.Request, rest string) {
	if _, ok := s.requireAPIAuth(w, r); !ok {
		return
	}
	cleanRest := cleanAPIPath(rest)
	pluginID, _, ok := strings.Cut(cleanRest, "/")
	if !ok || strings.TrimSpace(pluginID) == "" {
		http.NotFound(w, r)
		return
	}
	if !s.pluginEnabled(r.Context(), pluginID) {
		_ = s.stopBackendPlugin(pluginID)
		writeAPIError(w, http.StatusNotFound, "backend plugin is not enabled: "+pluginID)
		return
	}
	if _, ok, err := s.startBackendPlugin(r.Context(), pluginID); err != nil {
		s.serverError(w, err)
		return
	} else if !ok {
		writeAPIError(w, http.StatusNotFound, "backend plugin is not available: "+pluginID)
		return
	}
	if s.dispatchProtectedAPIPath(w, r, "plugins/"+cleanRest) {
		return
	}
	writeAPIError(w, http.StatusNotFound, "backend plugin route not found: "+cleanRest)
}

func (s *Server) startBackendPlugin(ctx context.Context, pluginID string) (plugins.BackendPlugin, bool, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" || s == nil {
		return nil, false, nil
	}
	if !s.pluginEnabled(ctx, pluginID) {
		_ = s.stopBackendPlugin(pluginID)
		return nil, false, nil
	}
	s.backendLifecycleMu.Lock()
	defer s.backendLifecycleMu.Unlock()
	if s.startedBackendPlugins == nil {
		s.startedBackendPlugins = map[string]plugins.BackendPlugin{}
	}
	if plugin := s.startedBackendPlugins[pluginID]; plugin != nil {
		return plugin, true, nil
	}
	plugin, ok, err := s.backendPlugin(pluginID)
	if err != nil {
		return nil, false, err
	}
	if !ok || plugin == nil {
		return nil, false, nil
	}
	log.Printf("debug backend plugin starting plugin_id=%s", pluginID)
	if err := plugin.Start(s); err != nil {
		_ = plugin.Stop(s)
		unregistered := s.protectedAPIRouteRegistry().unregisterPlugin(pluginID)
		log.Printf("debug backend plugin start failed plugin_id=%s routes_unregistered=%d error=%v", pluginID, unregistered, err)
		return nil, true, err
	}
	s.startedBackendPlugins[pluginID] = plugin
	log.Printf("debug backend plugin started plugin_id=%s", pluginID)
	return plugin, true, nil
}

func (s *Server) stopBackendPlugin(pluginID string) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" || s == nil {
		return nil
	}
	s.backendLifecycleMu.Lock()
	var plugin plugins.BackendPlugin
	if s.startedBackendPlugins != nil {
		plugin = s.startedBackendPlugins[pluginID]
		delete(s.startedBackendPlugins, pluginID)
	}
	s.backendLifecycleMu.Unlock()
	var err error
	if plugin != nil {
		log.Printf("debug backend plugin stopping plugin_id=%s", pluginID)
		err = plugin.Stop(s)
		if err != nil {
			log.Printf("debug backend plugin stop failed plugin_id=%s error=%v", pluginID, err)
		}
	}
	unregistered := s.protectedAPIRouteRegistry().unregisterPlugin(pluginID)
	if plugin != nil || unregistered > 0 {
		log.Printf("debug backend plugin stopped plugin_id=%s routes_unregistered=%d", pluginID, unregistered)
	}
	return err
}

func (s *Server) dispatchProtectedAPIPath(w http.ResponseWriter, r *http.Request, apiPath string) bool {
	route, ok := s.protectedAPIRouteRegistry().match(apiPath)
	if !ok {
		return false
	}
	if route.pluginID != "" && !s.pluginEnabled(r.Context(), route.pluginID) {
		_ = s.stopBackendPlugin(route.pluginID)
		return false
	}
	if _, ok := s.requireAPIAuth(w, r); !ok {
		return true
	}
	route.handler(s, cleanAPIPath(apiPath), w, r)
	return true
}

func (s *Server) backendPlugin(pluginID string) (plugins.BackendPlugin, bool, error) {
	if s == nil || s.backendPlugins == nil {
		return nil, false, nil
	}
	return s.backendPlugins.Plugin(pluginID)
}

func (s *Server) enabledBackendPlugins(ctx context.Context) ([]plugins.BackendPlugin, error) {
	if s == nil || s.backendPlugins == nil {
		return nil, nil
	}
	ids := s.backendPlugins.PluginIDs()
	out := make([]plugins.BackendPlugin, 0, len(ids))
	for _, pluginID := range ids {
		if !s.pluginEnabled(ctx, pluginID) {
			_ = s.stopBackendPlugin(pluginID)
			continue
		}
		plugin, ok, err := s.startBackendPlugin(ctx, pluginID)
		if err != nil {
			return nil, err
		}
		if ok && plugin != nil {
			out = append(out, plugin)
		}
	}
	return out, nil
}

func (s *Server) startAutoStartBackendPlugins(ctx context.Context) {
	if s == nil {
		return
	}
	for _, manifest := range s.pluginManifests {
		if !manifest.AutoStart || strings.TrimSpace(manifest.ID) == "" {
			continue
		}
		pluginID := manifest.ID
		if !s.pluginEnabled(ctx, pluginID) {
			continue
		}
		go func() {
			if _, _, err := s.startBackendPlugin(context.Background(), pluginID); err != nil {
				log.Printf("start backend plugin %s: %v", pluginID, err)
			}
		}()
	}
}

type protectedAPIRouteRegistry struct {
	mu     sync.RWMutex
	nextID uint64
	routes map[uint64]protectedAPIRouteEntry
}

type protectedAPIRouteEntry struct {
	id       uint64
	pluginID string
	path     string
	prefix   bool
	handler  plugins.ProtectedAPIHandler
}

type protectedAPIRouteHandle struct {
	registry *protectedAPIRouteRegistry
	id       uint64
	once     sync.Once
}

func newProtectedAPIRouteRegistry() *protectedAPIRouteRegistry {
	return &protectedAPIRouteRegistry{routes: map[uint64]protectedAPIRouteEntry{}}
}

func (s *Server) protectedAPIRouteRegistry() *protectedAPIRouteRegistry {
	if s.protectedAPIRoutes == nil {
		s.protectedAPIRoutes = newProtectedAPIRouteRegistry()
	}
	return s.protectedAPIRoutes
}

func (r *protectedAPIRouteRegistry) register(pluginID string, route plugins.ProtectedAPIRoute) (plugins.ProtectedAPIRouteHandle, error) {
	pluginID = strings.TrimSpace(pluginID)
	path := cleanAPIPath(route.Path)
	if pluginID == "" {
		return nil, fmt.Errorf("protected API route has no plugin id")
	}
	if path == "" || strings.Contains(path, "..") {
		return nil, fmt.Errorf("protected API route %q is invalid", route.Path)
	}
	if route.Handle == nil {
		return nil, fmt.Errorf("protected API route %q has no handler", path)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.routes {
		if existing.pluginID == pluginID && existing.path == path && existing.prefix == route.Prefix {
			return nil, fmt.Errorf("protected API route %q is already registered by %s", path, pluginID)
		}
	}
	r.nextID++
	id := r.nextID
	r.routes[id] = protectedAPIRouteEntry{
		id:       id,
		pluginID: pluginID,
		path:     path,
		prefix:   route.Prefix,
		handler:  route.Handle,
	}
	return &protectedAPIRouteHandle{registry: r, id: id}, nil
}

func (r *protectedAPIRouteRegistry) match(apiPath string) (protectedAPIRouteEntry, bool) {
	path := cleanAPIPath(apiPath)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, route := range r.routes {
		if !route.prefix && route.path == path {
			return route, true
		}
	}
	var best protectedAPIRouteEntry
	for _, route := range r.routes {
		if !route.prefix || !strings.HasPrefix(path, route.path+"/") {
			continue
		}
		if best.path == "" || len(route.path) > len(best.path) {
			best = route
		}
	}
	if best.path == "" {
		return protectedAPIRouteEntry{}, false
	}
	return best, true
}

func (r *protectedAPIRouteRegistry) unregister(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, id)
}

func (r *protectedAPIRouteRegistry) unregisterPlugin(pluginID string) int {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for id, route := range r.routes {
		if route.pluginID == pluginID {
			delete(r.routes, id)
			removed++
		}
	}
	return removed
}

func (h *protectedAPIRouteHandle) Unregister() {
	if h == nil || h.registry == nil {
		return
	}
	h.once.Do(func() {
		h.registry.unregister(h.id)
	})
}

func cleanAPIPath(value string) string {
	return strings.Trim(strings.TrimSpace(value), "/")
}
