package web

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"mailmirror/backend/plugins"
	bimibrandicons "mailmirror/backend/plugins/bimi_brand_icons"
	gravatarsendericons "mailmirror/backend/plugins/gravatar_sender_icons"
)

func (s *Server) pluginEnabled(ctx context.Context, pluginID string) bool {
	if s == nil || s.store == nil {
		return false
	}
	enabled, err := s.store.PluginEnabled(ctx, pluginID)
	return err == nil && enabled
}

func (s *Server) languageSearchEnabled(ctx context.Context) bool {
	return s.pluginEnabled(ctx, plugins.LanguageSearch)
}

func (s *Server) senderVisual(ctx context.Context, userID int64, sender string) (apiSenderVisual, bool) {
	email := senderEmail(sender)
	if email != "" {
		if icon, err := s.store.GetContactIconByEmailForUser(ctx, userID, email); err == nil && strings.TrimSpace(icon.BlobPath) != "" {
			return apiSenderVisual{
				PluginID: "contacts",
				Kind:     "contact",
				URL:      "/contacts/" + strconv.FormatInt(icon.ContactID, 10) + "/icon",
			}, true
		}
	}
	domain := senderDomain(sender)
	userDB, dbErr := s.store.UserDB(ctx, userID)
	if domain != "" && dbErr == nil && s.pluginEnabled(ctx, plugins.BIMIBrandIcons) {
		if icon, err := bimibrandicons.GetIcon(ctx, userDB, userID, domain); err == nil && icon.Status == "ok" && strings.TrimSpace(icon.SVG) != "" {
			return apiSenderVisual{
				PluginID: plugins.BIMIBrandIcons,
				Kind:     "brand",
				URL:      bimibrandicons.AssetURL(domain),
			}, true
		}
	}
	if email != "" && dbErr == nil && s.pluginEnabled(ctx, plugins.GravatarSenderIcons) {
		hash := gravatarsendericons.Hash(email)
		image, err := gravatarsendericons.GetImage(ctx, userDB, userID, hash)
		if err == nil && image.Status == "ok" && len(image.Image) > 0 {
			return apiSenderVisual{
				PluginID: plugins.GravatarSenderIcons,
				Kind:     "avatar",
				URL:      gravatarsendericons.AssetURL(hash),
			}, true
		}
		if err == nil && image.ExpiresAt.After(time.Now()) {
			return apiSenderVisual{}, false
		}
		if err == nil || err == sql.ErrNoRows {
			go s.refreshGravatarImage(userID, hash)
		}
	}
	return apiSenderVisual{}, false
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
