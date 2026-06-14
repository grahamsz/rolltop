package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
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

type mailListPageEntry struct {
	ETag       string
	Generation uint64
	Body       []byte
}

type mailListCache struct {
	mu          sync.Mutex
	generations map[int64]uint64
	entries     map[mailListCacheKey]mailListCacheEntry
	pages       map[mailListCacheKey]mailListPageEntry
}

func newMailListCache() *mailListCache {
	return &mailListCache{
		generations: map[int64]uint64{},
		entries:     map[mailListCacheKey]mailListCacheEntry{},
		pages:       map[mailListCacheKey]mailListPageEntry{},
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
	for key := range c.pages {
		if key.UserID == userID {
			delete(c.pages, key)
		}
	}
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

func (c *mailListCache) rememberPage(key mailListCacheKey, etag string, body []byte, generation uint64) {
	if c == nil || etag == "" || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generations[key.UserID] != generation {
		return
	}
	c.pages[key] = mailListPageEntry{ETag: etag, Generation: generation, Body: append([]byte(nil), body...)}
	c.entries[key] = mailListCacheEntry{ETag: etag, Generation: generation}
}

func (c *mailListCache) page(key mailListCacheKey) (mailListPageEntry, bool) {
	if c == nil {
		return mailListPageEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.pages[key]
	if !ok || entry.Generation != c.generations[key.UserID] {
		return mailListPageEntry{}, false
	}
	entry.Body = append([]byte(nil), entry.Body...)
	return entry, true
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

func (s *Server) rememberMailListPage(key mailListCacheKey, etag string, body []byte, generation uint64) {
	if s == nil || s.mailListCache == nil {
		return
	}
	s.mailListCache.rememberPage(key, etag, body, generation)
}

func (s *Server) writeMailListPageIfFresh(w http.ResponseWriter, r *http.Request, key mailListCacheKey) bool {
	if s == nil || s.mailListCache == nil || r.Method != http.MethodGet {
		return false
	}
	entry, ok := s.mailListCache.page(key)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", entry.ETag)
	if etagMatches(r.Header.Get("If-None-Match"), entry.ETag) {
		writeMailCacheTimingHeaders(w)
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	writeMailMemoryTimingHeaders(w)
	_, _ = w.Write(entry.Body)
	return true
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
	writeMailCacheTimingHeaders(w)
	w.WriteHeader(http.StatusNotModified)
	return true
}

func (s *Server) warmAllMailFirstPages(ctx context.Context) {
	if s == nil || s.store == nil || s.mailListCache == nil {
		return
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		log.Printf("warm all-mail first pages: %v", err)
		return
	}
	for _, user := range users {
		if err := s.warmAllMailFirstPage(ctx, user.ID); err != nil {
			log.Printf("warm all-mail first page user_id=%d: %v", user.ID, err)
		}
	}
}

func (s *Server) warmAllMailFirstPageAsync(userID int64) {
	if s == nil || s.store == nil || s.mailListCache == nil || userID <= 0 {
		return
	}
	s.mailWarmMu.Lock()
	if s.mailWarmRunning == nil {
		s.mailWarmRunning = map[int64]bool{}
	}
	if s.mailWarmRunning[userID] {
		s.mailWarmMu.Unlock()
		return
	}
	s.mailWarmRunning[userID] = true
	s.mailWarmMu.Unlock()
	go func() {
		defer func() {
			s.mailWarmMu.Lock()
			delete(s.mailWarmRunning, userID)
			s.mailWarmMu.Unlock()
		}()
		for {
			before := s.mailListGeneration(userID)
			if err := s.warmAllMailFirstPage(context.Background(), userID); err != nil {
				log.Printf("warm all-mail first page user_id=%d: %v", userID, err)
				return
			}
			if before == s.mailListGeneration(userID) {
				return
			}
		}
	}()
}

func (s *Server) warmAllMailFirstPage(ctx context.Context, userID int64) error {
	generation := s.mailListGeneration(userID)
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	response, err := s.mailPageResponse(ctx, user, 0, 1, newSearchTiming())
	if err != nil {
		return err
	}
	body, etag, err := cachedJSONBody(response)
	if err != nil {
		return err
	}
	s.rememberMailListPage(mailListCacheKey{UserID: userID, Page: 1}, etag, body, generation)
	return nil
}

func cachedJSONBody(value any) ([]byte, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	return append(raw, '\n'), etag, nil
}
