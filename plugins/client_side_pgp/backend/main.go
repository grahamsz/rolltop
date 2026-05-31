// File overview: Runtime-loaded entry point for the client-side PGP backend
// plugin. Keep this file focused on lifecycle and route registration; hook
// adapter methods live next to it and delegate behavior to subpackages.

package main

import (
	"fmt"
	"sync"

	"rolltop/backend/plugins"
	"rolltop/plugins/client_side_pgp/backend/api"
)

type pgpBackend struct {
	mu     sync.Mutex
	routes []plugins.ProtectedAPIRouteHandle
}

// RolltopPlugin is the symbol loaded by plugin.Open.
func RolltopPlugin() plugins.BackendPlugin {
	return &pgpBackend{}
}

func (pgpBackend) ID() string { return plugins.ClientSidePGP }

func (p *pgpBackend) Start(host plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.unregisterRoutesLocked()
	for _, route := range p.protectedAPIRoutes() {
		handle, err := host.RegisterProtectedAPI(p.ID(), route)
		if err != nil {
			p.unregisterRoutesLocked()
			return err
		}
		p.routes = append(p.routes, handle)
	}
	return nil
}

func (p *pgpBackend) Stop(plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unregisterRoutesLocked()
	return nil
}

func (p *pgpBackend) protectedAPIRoutes() []plugins.ProtectedAPIRoute {
	return []plugins.ProtectedAPIRoute{
		{Path: api.Path + "/private-keys", Handle: api.PrivateKeys},
		{Path: api.Path + "/private-keys", Prefix: true, Handle: api.PrivateKeyRoute},
		{Path: api.Path + "/public-keys", Handle: api.PublicKeys},
		{Path: api.Path + "/public-keys", Prefix: true, Handle: api.PublicKeyRoute},
	}
}

func (p *pgpBackend) unregisterRoutesLocked() {
	for _, handle := range p.routes {
		handle.Unregister()
	}
	p.routes = nil
}

func (p pgpBackend) String() string {
	return fmt.Sprintf("rolltop backend plugin %s", p.ID())
}
