package web

import (
	"bytes"
	"context"
	"io"
	"strings"

	"mailmirror/backend/mailparse"
	"mailmirror/backend/store"
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
	raw, err := s.rawMessageBytes(ctx, userID, msg)
	if err != nil {
		return htmlBody, textBody, fallbackIsPreviewOnly(textBody)
	}
	parsedText, parsedHTML, err := mailparse.ParseDisplayBody(bytes.NewReader(raw))
	if err != nil {
		return htmlBody, textBody, fallbackIsPreviewOnly(textBody)
	}
	if strings.TrimSpace(parsedHTML) != "" {
		htmlBody = parsedHTML
	}
	if strings.TrimSpace(parsedText) != "" {
		textBody = parsedText
	}
	return htmlBody, textBody, false
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
