package web

import (
	"net/http"
	"sync"
)

const mailListCacheMaxEntries = 512

// mailListCacheKey identifies one cached conversation-list response. Mailbox
// pages use MailboxID; search pages set Search plus normalized Query so a
// browser ETag is only reused for the exact same result slice.
type mailListCacheKey struct {
	UserID    int64
	MailboxID int64
	Page      int
	Search    bool
	Query     string
}

type mailListCacheEntry struct {
	ETag       string
	Generation uint64
}

type mailListCache struct {
	mu          sync.Mutex
	generations map[int64]uint64
	entries     map[mailListCacheKey]mailListCacheEntry
}

func newMailListCache() *mailListCache {
	return &mailListCache{
		generations: map[int64]uint64{},
		entries:     map[mailListCacheKey]mailListCacheEntry{},
	}
}

func (c *mailListCache) generation(userID int64) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generations[userID]
}

func (c *mailListCache) noteChanged(userID int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.generations[userID]++
	c.mu.Unlock()
}

func (c *mailListCache) remember(key mailListCacheKey, etag string, generation uint64) {
	if c == nil || etag == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= mailListCacheMaxEntries {
		for oldKey := range c.entries {
			delete(c.entries, oldKey)
			break
		}
	}
	c.entries[key] = mailListCacheEntry{ETag: etag, Generation: generation}
}

func (c *mailListCache) freshETag(key mailListCacheKey, ifNoneMatch string) (string, bool) {
	if c == nil || ifNoneMatch == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || entry.Generation != c.generations[key.UserID] {
		return "", false
	}
	if !etagMatches(ifNoneMatch, entry.ETag) {
		return "", false
	}
	return entry.ETag, true
}

func (s *Server) mailListGeneration(userID int64) uint64 {
	if s == nil || s.mailListCache == nil {
		return 0
	}
	return s.mailListCache.generation(userID)
}

func (s *Server) noteMailListChanged(userID int64) {
	if s == nil || s.mailListCache == nil {
		return
	}
	s.mailListCache.noteChanged(userID)
}

func (s *Server) rememberMailListETag(key mailListCacheKey, etag string, generation uint64) {
	if s == nil || s.mailListCache == nil {
		return
	}
	s.mailListCache.remember(key, etag, generation)
}

func (s *Server) writeMailListNotModifiedIfFresh(w http.ResponseWriter, r *http.Request, key mailListCacheKey) bool {
	if s == nil || s.mailListCache == nil || r.Method != http.MethodGet {
		return false
	}
	etag, ok := s.mailListCache.freshETag(key, r.Header.Get("If-None-Match"))
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusNotModified)
	return true
}
