package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"mailmirror/backend/mailparse"
	"mailmirror/backend/plugins"
	attachmentpreview "mailmirror/backend/plugins/attachment_preview"
	gravatarsendericons "mailmirror/backend/plugins/gravatar_sender_icons"
	"mailmirror/backend/store"
)

func (s *Server) handlePluginRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/plugins/"), "/")
	switch {
	case strings.HasPrefix(rest, "bimi_brand_icons/brand-icons/"):
		s.handleBrandIcon(w, r)
	case strings.HasPrefix(rest, "gravatar_sender_icons/avatar/"):
		s.handleGravatarAvatar(w, r, strings.TrimPrefix(rest, "gravatar_sender_icons/avatar/"))
	case strings.HasPrefix(rest, "attachment_preview/attachments/"):
		s.handleAttachmentPreview(w, r, strings.TrimPrefix(rest, "attachment_preview/attachments/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) attachmentPreview(att store.Attachment) *apiAttachmentPreview {
	preview, ok := attachmentpreview.ForAttachment(attachmentpreview.Attachment{
		ID:          att.ID,
		Filename:    att.Filename,
		ContentType: att.ContentType,
	})
	if !ok {
		return nil
	}
	return &apiAttachmentPreview{
		Available: preview.Available,
		Kind:      preview.Kind,
		URL:       preview.URL,
		Status:    preview.Status,
		PluginID:  preview.PluginID,
	}
}

func (s *Server) handleAttachmentPreview(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.AttachmentPreview) {
		http.NotFound(w, r)
		return
	}
	cu, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	rest = strings.TrimSuffix(rest, "/preview")
	id, err := strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	att, err := s.store.GetAttachmentForUser(r.Context(), cu.User.ID, id)
	if store.IsNotFound(err) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	kind := attachmentpreview.Kind(attachmentpreview.Attachment{ID: att.ID, Filename: att.Filename, ContentType: att.ContentType})
	if kind == "" {
		http.Error(w, "attachment preview is not supported", http.StatusUnsupportedMediaType)
		return
	}
	content, contentType, err := s.attachmentContentBytes(r.Context(), cu.User.ID, att, attachmentpreview.MaxBytes)
	if err != nil {
		http.Error(w, "attachment body is not available locally and could not be fetched from IMAP", http.StatusGone)
		return
	}
	contentType = attachmentpreview.CleanContentType(contentType)
	if kind == "image" && !attachmentpreview.SupportedImageType(contentType) {
		if guessed := attachmentpreview.ImageTypeFromName(att.Filename); guessed != "" {
			contentType = guessed
		}
	}
	if kind == "pdf" {
		contentType = "application/pdf"
	} else if !attachmentpreview.SupportedImageType(contentType) {
		http.Error(w, "attachment preview is not supported", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", path.Base(att.Filename)))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if kind == "image" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'none'; script-src 'none'; base-uri 'none'")
	}
	http.ServeContent(w, r, path.Base(att.Filename), time.Time{}, bytes.NewReader(content))
}

func (s *Server) attachmentContentBytes(ctx context.Context, userID int64, att store.Attachment, maxBytes int64) ([]byte, string, error) {
	if maxBytes <= 0 {
		maxBytes = attachmentpreview.MaxBytes
	}
	if strings.TrimSpace(att.BlobPath) != "" {
		if s.blobs == nil {
			return nil, "", store.ErrNotFound
		}
		file, err := s.blobs.OpenUserBlob(userID, att.BlobPath)
		if err != nil {
			return nil, "", err
		}
		defer file.Close()
		data, err := attachmentpreview.ReadLimited(file, maxBytes)
		return data, att.ContentType, err
	}
	msg, err := s.store.GetMessageForUser(ctx, userID, att.MessageID)
	if err != nil {
		return nil, "", err
	}
	raw, err := s.rawMessageBytes(ctx, userID, msg)
	if err != nil {
		return nil, "", err
	}
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return nil, "", err
	}
	file, ok := matchingAttachment(att, parsed.Files)
	if !ok {
		return nil, "", store.ErrNotFound
	}
	if int64(len(file.Data)) > maxBytes {
		return nil, "", fmt.Errorf("attachment exceeds preview limit")
	}
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = att.ContentType
	}
	return file.Data, contentType, nil
}

func (s *Server) handleGravatarAvatar(w http.ResponseWriter, r *http.Request, hash string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.pluginEnabled(r.Context(), plugins.GravatarSenderIcons) {
		http.NotFound(w, r)
		return
	}
	cu, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	hash = gravatarsendericons.NormalizeHash(hash)
	if hash == "" {
		http.NotFound(w, r)
		return
	}
	userDB, err := s.store.UserDB(r.Context(), cu.User.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	image, err := gravatarsendericons.GetImage(r.Context(), userDB, cu.User.ID, hash)
	if err != nil || image.Status != "ok" || len(image.Image) == 0 {
		http.NotFound(w, r)
		return
	}
	if image.ExpiresAt.Before(time.Now()) {
		go s.refreshGravatarImage(cu.User.ID, hash)
	}
	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:")
	_, _ = w.Write(image.Image)
}

func (s *Server) ensureGravatarImage(ctx context.Context, userID int64, hash string) (gravatarsendericons.Image, error) {
	userDB, err := s.store.UserDB(ctx, userID)
	if err != nil {
		return gravatarsendericons.Image{}, err
	}
	if image, err := gravatarsendericons.GetImage(ctx, userDB, userID, hash); err == nil && image.ExpiresAt.After(time.Now()) {
		return image, nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, gravatarsendericons.FetchURL(hash), nil)
	if err != nil {
		return gravatarsendericons.Image{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	now := time.Now().UTC()
	image := gravatarsendericons.Image{
		UserID:    userID,
		EmailHash: hash,
		Status:    "error",
		FetchedAt: now,
		ExpiresAt: gravatarsendericons.ErrorTTL(now),
		UpdatedAt: now,
	}
	if err != nil {
		image.Error = "fetch failed"
		_ = gravatarsendericons.UpsertImage(ctx, userDB, image)
		return image, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		image.Status = "missing"
		image.Error = "not found"
		image.ExpiresAt = gravatarsendericons.MissingTTL(now)
		_ = gravatarsendericons.UpsertImage(ctx, userDB, image)
		return image, store.ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		image.Error = "non-2xx response"
		_ = gravatarsendericons.UpsertImage(ctx, userDB, image)
		return image, fmt.Errorf("gravatar returned %d", resp.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !attachmentpreview.SupportedImageType(contentType) {
		image.Error = "unsupported image type"
		_ = gravatarsendericons.UpsertImage(ctx, userDB, image)
		return image, fmt.Errorf("unsupported gravatar content type")
	}
	data, err := attachmentpreview.ReadLimited(resp.Body, gravatarsendericons.MaxImageBytes)
	if err != nil {
		image.Error = "image too large"
		_ = gravatarsendericons.UpsertImage(ctx, userDB, image)
		return image, err
	}
	image.ContentType = contentType
	image.Image = data
	image.Status = "ok"
	image.Error = ""
	image.ExpiresAt = gravatarsendericons.PositiveTTL(now)
	if err := gravatarsendericons.UpsertImage(ctx, userDB, image); err != nil {
		return gravatarsendericons.Image{}, err
	}
	return image, nil
}
