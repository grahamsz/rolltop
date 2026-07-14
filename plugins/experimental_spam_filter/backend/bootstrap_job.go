package main

import "context"

func (p *spamFilterPlugin) reserveBootstrap(userID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bootstraps == nil {
		p.bootstraps = make(map[int64]context.CancelFunc)
	}
	if _, exists := p.bootstraps[userID]; exists {
		return false
	}
	p.bootstraps[userID] = func() {}
	return true
}

func (p *spamFilterPlugin) setBootstrapCancel(userID int64, cancel context.CancelFunc) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bootstraps == nil {
		return false
	}
	if _, exists := p.bootstraps[userID]; !exists {
		return false
	}
	p.bootstraps[userID] = cancel
	return true
}

func (p *spamFilterPlugin) releaseBootstrap(userID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.bootstraps, userID)
}

func (p *spamFilterPlugin) bootstrapActive(userID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, active := p.bootstraps[userID]
	return active
}

func (p *spamFilterPlugin) cancelBootstrap(userID int64) bool {
	p.mu.Lock()
	cancel, active := p.bootstraps[userID]
	p.mu.Unlock()
	if active && cancel != nil {
		cancel()
	}
	return active
}
