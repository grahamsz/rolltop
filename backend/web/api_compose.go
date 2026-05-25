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

func (s *Server) composeFormForRequest(r *http.Request) (composeForm, error) {
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
		return forwardComposeForm(msg), nil
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
		Attachments: composeSMTPAttachments(form.Attachments),
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
