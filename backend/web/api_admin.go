package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"mailmirror/backend/auth"
	remoteimageblocklist "mailmirror/backend/plugins/remote_image_blocklist"
	"mailmirror/backend/store"
)

func (s *Server) apiAdminUsers(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAdmin(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		users, err := s.store.ListUsers(r.Context())
		if err != nil {
			s.serverError(w, err)
			return
		}
		out := make([]apiUser, 0, len(users))
		for _, user := range users {
			out = append(out, safeUser(user))
		}
		writeJSON(w, map[string]any{"users": out})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in struct {
			Email    string `json:"email"`
			Name     string `json:"name"`
			Password string `json:"password"`
			IsAdmin  bool   `json:"is_admin"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		if len(in.Password) < 12 {
			writeAPIError(w, http.StatusBadRequest, "Password must be at least 12 characters.")
			return
		}
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			s.serverError(w, err)
			return
		}
		_, err = s.store.CreateUser(r.Context(), in.Email, in.Name, hash, in.IsAdmin)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not create user.")
			return
		}
		_ = cu
		writeJSON(w, map[string]any{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiAdminPlugins(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	settings, err := s.store.ListPluginSettings(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"plugins": apiPluginSettings(settings)})
}

func (s *Server) apiAdminPlugin(w http.ResponseWriter, r *http.Request, rest string) {
	if _, ok := s.requireAPIAdmin(w, r); !ok {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	pluginID := strings.Trim(rest, "/")
	if pluginID == "" || strings.Contains(pluginID, "/") {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.store.SetPluginEnabled(r.Context(), pluginID, in.Enabled); err != nil {
		if store.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	settings, err := s.store.ListPluginSettings(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "plugins": apiPluginSettings(settings)})
}

func (s *Server) apiAdminRemoteImageBlocklist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAPIAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rules, err := remoteimageblocklist.ListRules(r.Context(), s.store.DB())
		if err != nil {
			s.serverError(w, err)
			return
		}
		patterns := make([]string, 0, len(rules))
		for _, rule := range rules {
			if rule.Enabled {
				patterns = append(patterns, rule.Pattern)
			}
		}
		writeJSON(w, map[string]any{"patterns": patterns})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var in struct {
			Patterns []string `json:"patterns"`
			Text     string   `json:"text"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		patterns := in.Patterns
		if patterns == nil && strings.TrimSpace(in.Text) != "" {
			patterns = strings.Split(in.Text, "\n")
		}
		patterns, err := normalizeRemoteImageBlockPatterns(patterns)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := remoteimageblocklist.ReplaceRules(r.Context(), s.store.DB(), patterns); err != nil {
			s.serverError(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "patterns": patterns})
	default:
		methodNotAllowed(w)
	}
}

func normalizeRemoteImageBlockPatterns(patterns []string) ([]string, error) {
	const maxPatterns = 100
	const maxPatternLen = 500
	out := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		if len(pattern) > maxPatternLen {
			return nil, fmt.Errorf("Blocklist regex is too long; keep each pattern under %d characters.", maxPatternLen)
		}
		if seen[pattern] {
			continue
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("Invalid blocklist regex: %v", err)
		}
		seen[pattern] = true
		out = append(out, pattern)
		if len(out) > maxPatterns {
			return nil, fmt.Errorf("Keep the remote image blocklist under %d patterns.", maxPatterns)
		}
	}
	return out, nil
}
