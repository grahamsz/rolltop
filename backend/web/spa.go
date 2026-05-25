package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const frontendDistDir = "frontend/dist"

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
	http.ServeFile(w, r, full)
}

func isAppRoute(p string) bool {
	switch {
	case p == "/setup", p == "/login", p == "/mail", p == "/search", p == "/compose", p == "/settings/account", p == "/admin/users":
		return true
	case strings.HasPrefix(p, "/mail/"), strings.HasPrefix(p, "/mailbox/"), strings.HasPrefix(p, "/search/"):
		return true
	case strings.HasPrefix(p, "/messages/"), strings.HasPrefix(p, "/sync-runs/"), strings.HasPrefix(p, "/settings/account/"):
		return true
	default:
		return false
	}
}
