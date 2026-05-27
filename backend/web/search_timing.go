// File overview: Search response timing headers for production diagnostics.

package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type searchTiming struct {
	started time.Time
	filter  time.Duration
	sender  time.Duration
	bleve   time.Duration
	hydrate time.Duration
	render  time.Duration
	batches int
	rawHits int
	seeds   int
}

func newSearchTiming() *searchTiming {
	return &searchTiming{started: time.Now()}
}

func (t *searchTiming) measure(target *time.Duration) func() {
	start := time.Now()
	return func() {
		*target += time.Since(start)
	}
}

func writeSearchTimingHeaders(w http.ResponseWriter, timing *searchTiming, page int) {
	if timing == nil {
		return
	}
	total := time.Since(timing.started)
	w.Header().Set("Server-Timing", strings.Join([]string{
		serverTimingMetric("filter", timing.filter),
		serverTimingMetric("sender", timing.sender),
		serverTimingMetric("bleve", timing.bleve),
		serverTimingMetric("hydrate", timing.hydrate),
		serverTimingMetric("render", timing.render),
		serverTimingMetric("total", total),
	}, ", "))
	stats := strings.Join([]string{
		"cache=miss",
		"page=" + strconv.Itoa(page),
		"batches=" + strconv.Itoa(timing.batches),
		"raw_hits=" + strconv.Itoa(timing.rawHits),
		"seeds=" + strconv.Itoa(timing.seeds),
	}, ";")
	w.Header().Set("X-Rolltop-Search-Stats", stats)
	w.Header().Set("X-MailMirror-Search-Stats", stats)
}

func serverTimingMetric(name string, duration time.Duration) string {
	return fmt.Sprintf("%s;dur=%.1f", name, float64(duration.Microseconds())/1000)
}

func safeSearchHeaderToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func (s *Server) writeSearchNotModifiedIfFresh(w http.ResponseWriter, r *http.Request, key mailListCacheKey) bool {
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
	w.Header().Set("Server-Timing", `cache;desc="mail-list-etag";dur=0`)
	w.Header().Set("X-Rolltop-Search-Stats", "cache=hit")
	w.Header().Set("X-MailMirror-Search-Stats", "cache=hit")
	w.WriteHeader(http.StatusNotModified)
	return true
}
