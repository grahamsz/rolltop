// File overview: BIMI domain normalization, lookup records, and asset URL helpers.

package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func (s *Server) apiBrandIcons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.BIMIBrandIcons) {
		writeJSON(w, map[string]any{"icons": map[string]string{}})
		return
	}
	query := r.URL.Query()
	domains := parseBrandIconDomains(query["domain"], query.Get("domains"))
	icons := map[string]string{}
	hook, ok := bimiBrandIconsHook()
	if !ok {
		writeJSON(w, map[string]any{"icons": icons})
		return
	}
	for _, domain := range domains {
		icon, err := s.ensureBIMIIcon(r.Context(), cu.User.ID, domain)
		if err != nil || icon.Status != "ok" || strings.TrimSpace(icon.SVG) == "" {
			continue
		}
		icons[domain] = hook.AssetURL(domain)
	}
	writeJSON(w, map[string]any{"icons": icons})
}

func (s *Server) handleBrandIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cu, ok := current(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.BIMIBrandIcons) {
		http.NotFound(w, r)
		return
	}
	domain := strings.TrimPrefix(r.URL.Path, "/brand-icons/")
	domain = strings.TrimPrefix(domain, "/plugins/bimi_brand_icons/brand-icons/")
	domain = strings.TrimSuffix(domain, ".svg")
	hook, ok := bimiBrandIconsHook()
	if !ok {
		http.NotFound(w, r)
		return
	}
	domain = hook.NormalizeDomain(domain)
	if domain == "" {
		http.NotFound(w, r)
		return
	}
	userDB, err := s.store.UserDB(r.Context(), cu.User.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	icon, err := hook.GetIcon(r.Context(), userDB, cu.User.ID, domain)
	if store.IsNotFound(err) || err != nil || icon.Status != "ok" || strings.TrimSpace(icon.SVG) == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	_, _ = w.Write([]byte(icon.SVG))
}

func (s *Server) ensureBIMIIcon(ctx context.Context, userID int64, domain string) (plugins.BIMIIcon, error) {
	hook, ok := bimiBrandIconsHook()
	if !ok {
		return plugins.BIMIIcon{}, store.ErrNotFound
	}
	domain = hook.NormalizeDomain(domain)
	if domain == "" {
		return plugins.BIMIIcon{}, store.ErrNotFound
	}
	userDB, err := s.store.UserDB(ctx, userID)
	if err != nil {
		return plugins.BIMIIcon{}, err
	}
	if icon, err := hook.GetIcon(ctx, userDB, userID, domain); err == nil {
		if icon.Status == "ok" && icon.ExpiresAt.After(time.Now()) {
			return icon, nil
		}
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := hook.Fetch(fetchCtx, domain)
	if err != nil {
		return plugins.BIMIIcon{}, err
	}
	icon := plugins.BIMIIcon{
		UserID:    userID,
		Domain:    result.Domain,
		LogoURL:   result.LogoURL,
		SVG:       result.SVG,
		Status:    result.Status,
		Error:     result.Error,
		FetchedAt: result.FetchedAt,
		ExpiresAt: result.ExpiresAt,
		UpdatedAt: time.Now().UTC(),
	}
	if err := hook.UpsertIcon(ctx, userDB, icon); err != nil {
		return plugins.BIMIIcon{}, err
	}
	return icon, nil
}

func parseBrandIconDomains(domainValues []string, commaValues string) []string {
	hook, ok := bimiBrandIconsHook()
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, value := range append(domainValues, commaValues) {
		for _, raw := range strings.Split(value, ",") {
			domain := hook.NormalizeDomain(raw)
			if domain == "" || seen[domain] {
				continue
			}
			seen[domain] = true
			out = append(out, domain)
			if len(out) >= 40 {
				return out
			}
		}
	}
	return out
}
