// File overview: Web-layer plugin integration helpers.

package web

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rolltop/backend/plugins"
)

func (s *Server) pluginEnabled(ctx context.Context, pluginID string) bool {
	if s == nil || s.store == nil {
		return false
	}
	enabled, err := s.store.PluginEnabled(ctx, pluginID)
	if err != nil || enabled {
		return err == nil && enabled
	}
	manifest, ok := s.pluginManifest(pluginID)
	if !ok || !manifest.EnabledByDefault {
		return false
	}
	settings, err := s.store.ListPluginSettings(ctx)
	if err != nil {
		return false
	}
	for _, setting := range settings {
		if setting.ID == pluginID {
			return false
		}
	}
	return true
}

func (s *Server) languageSearchEnabled(ctx context.Context) bool {
	return s.pluginEnabled(ctx, plugins.LanguageSearch)
}

func (s *Server) availableThemes(ctx context.Context) []apiThemeDefinition {
	themes := []apiThemeDefinition{
		{ID: "classic", Name: "Classic"},
		{ID: "classic_dark", Name: "Classic Dark"},
	}
	if s == nil {
		return themes
	}
	for _, manifest := range s.pluginManifests {
		if !s.pluginEnabled(ctx, manifest.ID) {
			continue
		}
		for _, theme := range manifest.Themes {
			themes = append(themes, apiThemeDefinition{
				ID:       theme.ID,
				Name:     theme.Name,
				PluginID: manifest.ID,
				CSSURL:   pluginAssetPublicURL(manifest, theme.CSS),
			})
		}
	}
	return themes
}

func (s *Server) frontendPlugins(ctx context.Context) []apiFrontendPlugin {
	if s == nil {
		return nil
	}
	out := []apiFrontendPlugin{}
	for _, manifest := range s.pluginManifests {
		if !s.pluginEnabled(ctx, manifest.ID) || manifest.Frontend == nil || strings.TrimSpace(manifest.Frontend.Module) == "" {
			continue
		}
		plugin := apiFrontendPlugin{
			ID:        manifest.ID,
			Name:      manifest.Name,
			Version:   manifest.Version,
			ModuleURL: pluginAssetPublicURL(manifest, manifest.Frontend.Module),
		}
		if strings.TrimSpace(manifest.Frontend.CSS) != "" {
			plugin.CSSURL = pluginAssetPublicURL(manifest, manifest.Frontend.CSS)
		}
		out = append(out, plugin)
	}
	return out
}

func pluginAssetPublicURL(manifest plugins.Manifest, asset string) string {
	asset = strings.TrimLeft(strings.TrimSpace(asset), "/")
	out := "/plugins/" + manifest.ID + "/assets/" + asset
	if version := pluginAssetVersion(manifest, asset); version != "" {
		out += "?v=" + url.QueryEscape(version)
	}
	return out
}

func pluginAssetVersion(manifest plugins.Manifest, asset string) string {
	if manifest.Dir != "" && asset != "" {
		full := filepath.Join(manifest.Dir, filepath.FromSlash(asset))
		if info, err := os.Stat(full); err == nil {
			return strconv.FormatInt(info.ModTime().UnixNano(), 36)
		}
	}
	return strings.TrimSpace(manifest.Version)
}

func (s *Server) pluginManifest(id string) (plugins.Manifest, bool) {
	id = strings.TrimSpace(id)
	if s == nil || id == "" {
		return plugins.Manifest{}, false
	}
	for _, manifest := range s.pluginManifests {
		if manifest.ID == id {
			return manifest, true
		}
	}
	return plugins.Manifest{}, false
}

func (s *Server) senderVisual(ctx context.Context, userID int64, sender string) (apiSenderVisual, bool) {
	userDB, _ := s.store.UserDB(ctx, userID)
	return s.senderVisualWithOptions(ctx, userID, sender, senderVisualOptions{
		userDB:          userDB,
		bimiEnabled:     s.pluginEnabled(ctx, plugins.BIMIBrandIcons),
		gravatarEnabled: s.pluginEnabled(ctx, plugins.GravatarSenderIcons),
	})
}

type senderVisualOptions struct {
	userDB          *sql.DB
	bimiEnabled     bool
	gravatarEnabled bool
	cache           map[string]senderVisualCacheEntry
}

type senderVisualCacheEntry struct {
	visual apiSenderVisual
	ok     bool
}

func (s *Server) senderVisualWithOptions(ctx context.Context, userID int64, sender string, opts senderVisualOptions) (apiSenderVisual, bool) {
	email := senderEmail(sender)
	domain := senderDomain(sender)
	cacheKey := ""
	if opts.cache != nil {
		cacheKey = strings.ToLower(email) + "|" + strings.ToLower(domain)
		if entry, ok := opts.cache[cacheKey]; ok {
			return entry.visual, entry.ok
		}
	}
	cacheResult := func(visual apiSenderVisual, ok bool) (apiSenderVisual, bool) {
		if opts.cache != nil {
			opts.cache[cacheKey] = senderVisualCacheEntry{visual: visual, ok: ok}
		}
		return visual, ok
	}
	if email != "" {
		if icon, err := s.store.GetContactIconByEmailForUser(ctx, userID, email); err == nil && strings.TrimSpace(icon.BlobPath) != "" {
			return cacheResult(apiSenderVisual{
				PluginID: "contacts",
				Kind:     "contact",
				URL:      "/contacts/" + strconv.FormatInt(icon.ContactID, 10) + "/icon",
			}, true)
		}
	}
	if domain != "" && opts.userDB != nil && opts.bimiEnabled {
		if hook, ok := bimiBrandIconsHook(); ok {
			if icon, err := senderBIMIIconMeta(ctx, hook, opts.userDB, userID, domain); err == nil && icon.Status == "ok" && icon.HasSVG {
				return cacheResult(apiSenderVisual{
					PluginID: plugins.BIMIBrandIcons,
					Kind:     "brand",
					URL:      hook.AssetURL(domain),
				}, true)
			}
		}
	}
	if email != "" && opts.userDB != nil && opts.gravatarEnabled {
		if hook, ok := gravatarSenderIconsHook(); ok {
			hash := hook.Hash(email)
			image, err := senderGravatarImageMeta(ctx, hook, opts.userDB, userID, hash)
			if err == nil && image.Status == "ok" && image.HasImage {
				return cacheResult(apiSenderVisual{
					PluginID: plugins.GravatarSenderIcons,
					Kind:     "avatar",
					URL:      hook.AssetURL(hash),
				}, true)
			}
			if err == nil && image.ExpiresAt.After(time.Now()) {
				return cacheResult(apiSenderVisual{}, false)
			}
			if err == nil || err == sql.ErrNoRows {
				go s.refreshGravatarImage(userID, hash)
			}
		}
	}
	return cacheResult(apiSenderVisual{}, false)
}

func senderBIMIIconMeta(ctx context.Context, hook plugins.BIMIHook, db *sql.DB, userID int64, domain string) (plugins.BIMIIconMeta, error) {
	if metaHook, ok := hook.(plugins.BIMIIconMetaHook); ok {
		return metaHook.GetIconMeta(ctx, db, userID, domain)
	}
	icon, err := hook.GetIcon(ctx, db, userID, domain)
	if err != nil {
		return plugins.BIMIIconMeta{}, err
	}
	return plugins.BIMIIconMeta{
		ID:        icon.ID,
		UserID:    icon.UserID,
		Domain:    icon.Domain,
		LogoURL:   icon.LogoURL,
		Status:    icon.Status,
		Error:     icon.Error,
		HasSVG:    strings.TrimSpace(icon.SVG) != "",
		FetchedAt: icon.FetchedAt,
		ExpiresAt: icon.ExpiresAt,
		UpdatedAt: icon.UpdatedAt,
	}, nil
}

func senderGravatarImageMeta(ctx context.Context, hook plugins.GravatarHook, db *sql.DB, userID int64, hash string) (plugins.GravatarImageMeta, error) {
	if metaHook, ok := hook.(plugins.GravatarImageMetaHook); ok {
		return metaHook.GetImageMeta(ctx, db, userID, hash)
	}
	image, err := hook.GetImage(ctx, db, userID, hash)
	if err != nil {
		return plugins.GravatarImageMeta{}, err
	}
	return plugins.GravatarImageMeta{
		ID:          image.ID,
		UserID:      image.UserID,
		EmailHash:   image.EmailHash,
		ContentType: image.ContentType,
		Status:      image.Status,
		Error:       image.Error,
		HasImage:    len(image.Image) > 0,
		FetchedAt:   image.FetchedAt,
		ExpiresAt:   image.ExpiresAt,
		UpdatedAt:   image.UpdatedAt,
	}, nil
}

func (s *Server) refreshGravatarImage(userID int64, hash string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, _ = s.ensureGravatarImage(ctx, userID, hash)
}

func senderDomain(value string) string {
	email := senderEmail(value)
	if email == "" {
		return ""
	}
	at := strings.LastIndex(email, "@")
	if at < 0 || at+1 >= len(email) {
		return ""
	}
	return strings.ToLower(strings.Trim(email[at+1:], " \t\r\n<>.,;:\"'()[]"))
}
