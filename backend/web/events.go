// File overview: Server-sent event helpers for sync and UI status updates.

package web

import "sync"

type eventHub struct {
	mu          sync.Mutex
	subscribers map[int64]map[chan struct{}]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subscribers: map[int64]map[chan struct{}]struct{}{}}
}

func (h *eventHub) Subscribe(userID int64) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subscribers[userID] == nil {
		h.subscribers[userID] = map[chan struct{}]struct{}{}
	}
	h.subscribers[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers[userID], ch)
		if len(h.subscribers[userID]) == 0 {
			delete(h.subscribers, userID)
		}
		h.mu.Unlock()
		close(ch)
	}
}

// Notify is intentionally lossy. A subscriber only needs to know that chrome
// state changed; the SSE handler reloads the latest server-side snapshot.
func (h *eventHub) Notify(userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers[userID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
