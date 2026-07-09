// File overview: Sanitization and presentation helpers for message bodies.

package web

import (
	"bytes"
	"context"
	"io"
	"strings"

	"rolltop/backend/mailparse"
	"rolltop/backend/plugins"
	"rolltop/backend/store"
)

func (s *Server) displayBodiesForMessage(ctx context.Context, userID int64, msg store.MessageRecord) (string, string, bool) {
	htmlBody := msg.BodyHTML
	textBody := msg.BodyText
	if strings.TrimSpace(htmlBody) != "" {
		return htmlBody, textBody, false
	}
	select {
	case <-ctx.Done():
		return htmlBody, textBody, fallbackIsPreviewOnly(textBody)
	default:
	}
	if !msg.IsEncrypted && !msg.IsSigned {
		hasSecurityProvider, err := s.hasMessageSecurityProvider(ctx)
		if err == nil && !hasSecurityProvider {
			if parsedText, parsedHTML, ok := s.displayBodyFromBlob(ctx, userID, msg); ok {
				htmlBody, textBody = s.persistDisplayBodies(ctx, userID, msg, htmlBody, textBody, parsedHTML, parsedText)
				return htmlBody, textBody, false
			}
		}
	}
	storedText, storedHTML, storedOK := s.storedDisplayBodies(ctx, userID, msg)
	if strings.TrimSpace(storedHTML) != "" {
		return storedHTML, storedText, false
	}
	raw, err := s.rawMessageBytes(ctx, userID, msg)
	if err != nil {
		if storedOK {
			return storedHTML, storedText, fallbackIsPreviewOnly(storedText)
		}
		return htmlBody, textBody, fallbackIsPreviewOnly(textBody)
	}
	parsedText, parsedHTML, err := mailparse.ParseDisplayBody(bytes.NewReader(raw))
	if err != nil {
		return htmlBody, textBody, fallbackIsPreviewOnly(textBody)
	}
	state := plugins.MessageSecurityState{Encrypted: msg.IsEncrypted, Signed: msg.IsSigned}
	if detected, handled, err := s.detectMessageSecurity(ctx, userID, raw, plugins.MessageBody{Purpose: "display", Text: parsedText, HTML: parsedHTML}); err == nil && handled {
		state.Encrypted = state.Encrypted || detected.Encrypted
		state.Signed = state.Signed || detected.Signed
	}
	if transform, err := s.transformMessageSecurityBody(ctx, userID, raw, state, plugins.MessageBody{Purpose: "display", Text: parsedText, HTML: parsedHTML}); err == nil && transform.Applied {
		parsedText = transform.Body.Text
		parsedHTML = transform.Body.HTML
	}
	htmlBody, textBody = s.persistDisplayBodies(ctx, userID, msg, htmlBody, textBody, parsedHTML, parsedText)
	return htmlBody, textBody, false
}

func (s *Server) storedDisplayBodies(ctx context.Context, userID int64, msg store.MessageRecord) (string, string, bool) {
	if s == nil || s.store == nil || msg.ID <= 0 {
		return "", "", false
	}
	text, htmlBody, err := s.store.GetMessageBodiesForUser(ctx, userID, msg.ID)
	if err != nil {
		return "", "", false
	}
	return text, htmlBody, strings.TrimSpace(text) != "" || strings.TrimSpace(htmlBody) != ""
}

func (s *Server) displayBodyFromBlob(ctx context.Context, userID int64, msg store.MessageRecord) (string, string, bool) {
	if s.blobs == nil || strings.TrimSpace(msg.BlobPath) == "" {
		return "", "", false
	}
	select {
	case <-ctx.Done():
		return "", "", false
	default:
	}
	f, err := s.blobs.OpenUserBlob(userID, msg.BlobPath)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	parsedText, parsedHTML, err := mailparse.ParseDisplayBody(f)
	if err != nil {
		return "", "", false
	}
	return parsedText, parsedHTML, true
}

func (s *Server) persistDisplayBodies(ctx context.Context, userID int64, msg store.MessageRecord, htmlBody, textBody, parsedHTML, parsedText string) (string, string) {
	if strings.TrimSpace(parsedHTML) != "" {
		htmlBody = parsedHTML
	}
	if strings.TrimSpace(parsedText) != "" {
		textBody = parsedText
	}
	if s.store != nil && msg.ID > 0 && (strings.TrimSpace(htmlBody) != "" || strings.TrimSpace(textBody) != "") {
		_ = s.store.UpdateMessageBodies(ctx, userID, msg.ID, textBody, htmlBody)
	}
	return htmlBody, textBody
}

func fallbackIsPreviewOnly(textBody string) bool {
	return strings.TrimSpace(textBody) != ""
}

func (s *Server) rawMessageBytes(ctx context.Context, userID int64, msg store.MessageRecord) ([]byte, error) {
	if s.blobs != nil && strings.TrimSpace(msg.BlobPath) != "" {
		f, err := s.blobs.OpenUserBlob(userID, msg.BlobPath)
		if err == nil {
			defer f.Close()
			raw, err := io.ReadAll(f)
			if err == nil {
				return raw, nil
			}
		}
	}
	if s.syncer == nil {
		return nil, store.ErrNotFound
	}
	return s.syncer.FetchAndCacheRawMessageForMessage(ctx, userID, msg)
}
