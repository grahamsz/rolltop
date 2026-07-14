// File overview: Lifecycle, API registration, and model ownership for the
// advisory experimental spam-filter runtime plugin.

package main

import (
	"context"
	"sync"

	"rolltop/backend/plugins"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

type spamFilterPlugin struct {
	mu           sync.Mutex
	routes       []plugins.ProtectedAPIRouteHandle
	classifier   *spammodel.Classifier
	modelLoaded  bool
	modelVersion string
	modelError   string
	backfills    map[int64]context.CancelFunc
	bootstraps   map[int64]context.CancelFunc
}

// RolltopPlugin is loaded by the runtime Go-plugin host.
func RolltopPlugin() plugins.BackendPlugin {
	return &spamFilterPlugin{}
}

func (*spamFilterPlugin) ID() string { return pluginID }

func (p *spamFilterPlugin) Start(host plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	p.loadModelLocked()
	p.backfills = make(map[int64]context.CancelFunc)
	p.bootstraps = make(map[int64]context.CancelFunc)

	handle, err := host.RegisterProtectedAPI(pluginID, plugins.ProtectedAPIRoute{
		Path: apiPath, Prefix: true, Handle: p.handleAPI,
	})
	if err != nil {
		p.stopLocked()
		return err
	}
	p.routes = append(p.routes, handle)
	return nil
}

func (p *spamFilterPlugin) Stop(plugins.BackendStartHost) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

func (p *spamFilterPlugin) stopLocked() {
	for _, cancel := range p.backfills {
		cancel()
	}
	p.backfills = nil
	for _, cancel := range p.bootstraps {
		cancel()
	}
	p.bootstraps = nil
	for _, route := range p.routes {
		route.Unregister()
	}
	p.routes = nil
}

func (p *spamFilterPlugin) model() (*spammodel.Classifier, string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadModelLocked()
	return p.classifier, p.modelVersion, p.modelError
}

func (p *spamFilterPlugin) loadModelLocked() {
	if p.modelLoaded {
		return
	}
	p.modelLoaded = true
	classifier, err := spammodel.LoadEmbedded()
	if err != nil {
		p.classifier = nil
		p.modelVersion = ""
		p.modelError = safeError(err.Error())
		return
	}
	p.classifier = classifier
	p.modelVersion = classifier.ModelVersion()
	p.modelError = ""
}

var (
	_ plugins.BackendPlugin             = (*spamFilterPlugin)(nil)
	_ plugins.MessageClassifier         = (*spamFilterPlugin)(nil)
	_ plugins.MessageAnnotationProvider = (*spamFilterPlugin)(nil)
	_ plugins.MessageMoveObserver       = (*spamFilterPlugin)(nil)
)
