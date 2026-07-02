// File overview: Static frontend and SPA fallback serving.

package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const frontendDistDir = "frontend/dist"
const immutableFrontendAssetCacheControl = "public, max-age=31536000, immutable"

type androidLatestMetadata struct {
	VersionCode int    `json:"versionCode"`
	VersionName string `json:"versionName"`
	APKURL      string `json:"apkUrl"`
	SHA256      string `json:"sha256,omitempty"`
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if r.URL.Path != "/" && !isAppRoute(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(frontendDistDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.Error(w, "frontend has not been built; run npm run build", http.StatusServiceUnavailable)
		return
	}
	http.ServeFile(w, r, index)
}

func (s *Server) handleFrontendAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(frontendDistDir, clean)
	if _, err := os.Stat(full); err != nil {
		http.NotFound(w, r)
		return
	}
	if isImmutableFrontendAsset(clean) {
		w.Header().Set("Cache-Control", immutableFrontendAssetCacheControl)
	}
	http.ServeFile(w, r, full)
}

func (s *Server) handleAndroidLatest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	full := filepath.Join(frontendDistDir, "android", "latest.json")
	data, err := os.ReadFile(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var metadata androidLatestMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		http.Error(w, "invalid android update metadata", http.StatusInternalServerError)
		return
	}
	metadata.APKURL = publicRequestBaseURL(r) + "/android/rolltop.apk"
	writeJSON(w, metadata)
}

func publicRequestBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if forwardedHost := r.Header.Get("X-Forwarded-Host"); forwardedHost != "" {
		return scheme + "://" + forwardedHost
	}
	return scheme + "://" + r.Host
}

func isImmutableFrontendAsset(cleanPath string) bool {
	cleanPath = filepath.ToSlash(filepath.Clean(cleanPath))
	if !strings.HasPrefix(cleanPath, "assets/") {
		return false
	}
	switch strings.ToLower(filepath.Ext(cleanPath)) {
	case ".js", ".css":
		return true
	default:
		return false
	}
}

func isAppRoute(p string) bool {
	switch {
	case p == "/setup", p == "/login", p == "/mail", p == "/search", p == "/compose", p == "/contacts", p == "/settings/account", p == "/admin/users":
		return true
	case strings.HasPrefix(p, "/mail/"), strings.HasPrefix(p, "/mailbox/"), strings.HasPrefix(p, "/search/"):
		return true
	case strings.HasPrefix(p, "/messages/"), strings.HasPrefix(p, "/sync-runs/"), strings.HasPrefix(p, "/settings/account/"), strings.HasPrefix(p, "/contacts/"):
		return true
	default:
		return false
	}
}
