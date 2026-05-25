// File overview: Compose, reply, forward, attachment upload, and send API handlers.

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mailmirror/backend/smtpclient"
	"mailmirror/backend/store"
)

const (
	composeMaxUploadBytes  int64 = 80 << 20
	composeMaxRequestBytes int64 = 96 << 20
)

func (s *Server) apiCompose(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		form, err := s.composeFormForRequest(r)
		if err != nil {
			if store.IsNotFound(err) {
				http.NotFound(w, r)
				return
			}
			s.serverError(w, err)
			return
		}
		identities := s.composeIdentities(r.Context(), cu)
		writeJSON(w, map[string]any{"compose": form, "compose_from": s.composeFromLabel(r.Context(), cu), "from_identities": identities})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		form, ok := decodeComposePost(w, r)
		if !ok {
			return
		}
		sent, err := s.sendCompose(r.Context(), cu, form)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Could not send message.")
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message_id": sent.ID})
	default:
		methodNotAllowed(w)
	}
}
func decodeComposePost(w http.ResponseWriter, r *http.Request) (composeForm, bool) {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		return decodeComposeMultipart(w, r)
	}
	var form composeForm
	if !decodeJSON(w, r, &form) {
		return composeForm{}, false
	}
	return form, true
}

func decodeComposeMultipart(w http.ResponseWriter, r *http.Request) (composeForm, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, composeMaxRequestBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "Attachment upload is too large.")
		return composeForm{}, false
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	payload := strings.TrimSpace(r.FormValue("payload"))
	if payload == "" {
		writeAPIError(w, http.StatusBadRequest, "Compose payload is missing.")
		return composeForm{}, false
	}
	var form composeForm
	if err := json.Unmarshal([]byte(payload), &form); err != nil {
		writeAPIError(w, http.StatusBadRequest, "Compose payload is invalid.")
		return composeForm{}, false
	}
	var total int64
	for i := range form.Attachments {
		meta := &form.Attachments[i]
		meta.Field = strings.TrimSpace(meta.Field)
		if meta.Field == "" {
			meta.Field = fmt.Sprintf("attachment_%d", i)
		}
		files := r.MultipartForm.File[meta.Field]
		if len(files) == 0 {
			writeAPIError(w, http.StatusBadRequest, "Attachment upload is missing.")
			return composeForm{}, false
		}
		file, err := files[0].Open()
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "Attachment upload could not be read.")
			return composeForm{}, false
		}
		remaining := composeMaxUploadBytes - total
		data, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
		_ = file.Close()
		if readErr != nil {
			writeAPIError(w, http.StatusBadRequest, "Attachment upload could not be read.")
			return composeForm{}, false
		}
		if int64(len(data)) > remaining {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "Attachment upload is too large.")
			return composeForm{}, false
		}
		total += int64(len(data))
		meta.Data = data
		meta.Size = int64(len(data))
		if strings.TrimSpace(meta.Filename) == "" {
			meta.Filename = files[0].Filename
		}
		if strings.TrimSpace(meta.Filename) == "" {
			meta.Filename = "attachment"
		}
		meta.ContentType = normalizeUploadContentType(meta.ContentType, files[0].Header.Get("Content-Type"), data)
		if meta.Inline && strings.TrimSpace(meta.ContentID) == "" {
			meta.ContentID = fmt.Sprintf("mailmirror-inline-%d", i)
		}
	}
	return form, true
}

func normalizeUploadContentType(metaType, headerType string, data []byte) string {
	contentType := strings.TrimSpace(metaType)
	if contentType == "" {
		contentType = strings.TrimSpace(headerType)
	}
	if contentType == "" || strings.EqualFold(contentType, "application/octet-stream") {
		contentType = http.DetectContentType(data)
	}
	return contentType
}

func composeSMTPAttachments(items []composeAttachment) []smtpclient.Attachment {
	attachments := make([]smtpclient.Attachment, 0, len(items))
	for _, item := range items {
		attachments = append(attachments, smtpclient.Attachment{
			Filename:    item.Filename,
			ContentType: item.ContentType,
			ContentID:   item.ContentID,
			Inline:      item.Inline,
			Data:        item.Data,
		})
	}
	return attachments
}

func composeUploadedAttachmentBytes(items []composeAttachment) int64 {
	var total int64
	for _, item := range items {
		if item.Size > 0 {
			total += item.Size
			continue
		}
		total += int64(len(item.Data))
	}
	return total
}

func composeExistingAttachmentIDs(items []composeExistingAttachment) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if item.ID > 0 {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func (s *Server) composeExistingAttachmentsForMessage(ctx context.Context, userID, messageID int64) ([]composeExistingAttachment, error) {
	attachments, err := s.store.ListAttachmentsForMessage(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	attachments = visibleAttachments(attachments)
	out := make([]composeExistingAttachment, 0, len(attachments))
	for _, att := range attachments {
		out = append(out, composeExistingAttachment{
			ID:          att.ID,
			Filename:    attachmentDisplayName(att),
			ContentType: att.ContentType,
			Size:        att.Size,
			DownloadURL: fmt.Sprintf("/attachments/%d/download", att.ID),
		})
	}
	return out, nil
}

func (s *Server) composeSMTPExistingAttachments(ctx context.Context, userID int64, ids []int64, remaining int64) ([]smtpclient.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if remaining <= 0 {
		return nil, fmt.Errorf("attachments exceed compose limit")
	}
	seen := map[int64]bool{}
	attachments := make([]smtpclient.Attachment, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		att, err := s.store.GetAttachmentForUser(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		if !isDisplayAttachment(att) {
			continue
		}
		if remaining <= 0 {
			return nil, fmt.Errorf("attachments exceed compose limit")
		}
		data, contentType, err := s.attachmentContentBytes(ctx, userID, att, remaining)
		if err != nil {
			return nil, fmt.Errorf("load attachment %s: %w", attachmentDisplayName(att), err)
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return nil, fmt.Errorf("attachments exceed compose limit")
		}
		filename := attachmentDisplayName(att)
		if filename == "" {
			filename = "attachment"
		}
		contentType = strings.TrimSpace(contentType)
		if contentType == "" {
			contentType = strings.TrimSpace(att.ContentType)
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		attachments = append(attachments, smtpclient.Attachment{
			Filename:    filename,
			ContentType: contentType,
			Inline:      false,
			Data:        data,
		})
	}
	return attachments, nil
}

func (s *Server) composeFormForRequest(r *http.Request) (composeForm, error) {
	if raw := strings.TrimSpace(r.URL.Query().Get("reply_all")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return composeForm{}, store.ErrNotFound
		}
		cu, _ := current(r)
		msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
		if err != nil {
			return composeForm{}, err
		}
		thread, err := s.store.ListThreadMessagesForUser(r.Context(), cu.User.ID, msg)
		if err != nil {
			return composeForm{}, err
		}
		form := replyAllComposeForm(msg, thread, s.ownAddresses(r.Context(), cu.User))
		form.FromIdentityID = s.replyFromIdentityID(r.Context(), cu, msg, thread)
		attachments, err := s.composeExistingAttachmentsForMessage(r.Context(), cu.User.ID, msg.ID)
		if err != nil {
			return composeForm{}, err
		}
		form.AvailableAttachments = attachments
		return form, nil
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("reply")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return composeForm{}, store.ErrNotFound
		}
		cu, _ := current(r)
		msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
		if err != nil {
			return composeForm{}, err
		}
		thread, err := s.store.ListThreadMessagesForUser(r.Context(), cu.User.ID, msg)
		if err != nil {
			return composeForm{}, err
		}
		form := replyComposeForm(msg, thread, s.ownAddresses(r.Context(), cu.User))
		form.FromIdentityID = s.replyFromIdentityID(r.Context(), cu, msg, thread)
		attachments, err := s.composeExistingAttachmentsForMessage(r.Context(), cu.User.ID, msg.ID)
		if err != nil {
			return composeForm{}, err
		}
		form.AvailableAttachments = attachments
		return form, nil
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("forward")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return composeForm{}, store.ErrNotFound
		}
		cu, _ := current(r)
		msg, err := s.store.GetMessageForUser(r.Context(), cu.User.ID, id)
		if err != nil {
			return composeForm{}, err
		}
		form := forwardComposeForm(msg)
		attachments, err := s.composeExistingAttachmentsForMessage(r.Context(), cu.User.ID, msg.ID)
		if err != nil {
			return composeForm{}, err
		}
		form.AvailableAttachments = attachments
		form.IncludeAttachmentIDs = composeExistingAttachmentIDs(attachments)
		return form, nil
	}
	return composeForm{}, nil
}

func (s *Server) sendCompose(ctx context.Context, cu currentUser, form composeForm) (store.MessageRecord, error) {
	if s.sender == nil {
		return store.MessageRecord{}, errors.New("SMTP sending is not configured")
	}
	account, err := s.store.GetMailAccount(ctx, cu.User.ID)
	if err != nil {
		return store.MessageRecord{}, err
	}
	identity, err := s.selectedComposeIdentity(ctx, cu, form.FromIdentityID)
	if err != nil {
		return store.MessageRecord{}, err
	}
	uploadedAttachments := composeSMTPAttachments(form.Attachments)
	existingAttachments, err := s.composeSMTPExistingAttachments(ctx, cu.User.ID, form.IncludeAttachmentIDs, composeMaxUploadBytes-composeUploadedAttachmentBytes(form.Attachments))
	if err != nil {
		return store.MessageRecord{}, err
	}
	attachments := append(uploadedAttachments, existingAttachments...)
	msg := smtpclient.Message{
		From:        identity.Header,
		To:          []string{form.To},
		Cc:          []string{form.Cc},
		Bcc:         []string{form.Bcc},
		Subject:     form.Subject,
		BodyText:    form.Body,
		BodyHTML:    form.BodyHTML,
		MessageID:   smtpclient.NewMessageID(identity.Email),
		Date:        time.Now(),
		Attachments: attachments,
	}
	if form.InReplyToID > 0 {
		reply, err := s.store.GetMessageForUser(ctx, cu.User.ID, form.InReplyToID)
		if err != nil && !store.IsNotFound(err) {
			return store.MessageRecord{}, err
		}
		if err == nil {
			msg.InReplyTo = reply.MessageIDHeader
			msg.References = referencesForReply(reply)
		}
	}
	raw, err := s.sender.Send(ctx, account, msg)
	if err != nil {
		return store.MessageRecord{}, err
	}
	return s.storeSentMessage(ctx, cu.User.ID, account, msg, form, raw)
}
