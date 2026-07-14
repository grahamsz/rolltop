package main

import (
	"strings"

	"rolltop/backend/mailparse"
	spammodel "rolltop/plugins/experimental_spam_filter/model"
)

type bootstrapParsedMessage struct {
	Model       spammodel.Message
	MessageID   string
	From        string
	To          string
	Subject     string
	IsEncrypted bool
}

func parseBootstrapMessage(raw []byte, envelope bootstrapEnvelope) (bootstrapParsedMessage, error) {
	parsed, err := mailparse.Parse(raw)
	if err != nil {
		return bootstrapParsedMessage{}, err
	}
	from := firstBootstrapValue(parsed.From, envelope.From)
	to := firstBootstrapValue(parsed.To, envelope.To)
	subject := firstBootstrapValue(parsed.Subject, envelope.Subject)
	messageID := firstBootstrapValue(parsed.MessageID, envelope.MessageID)
	body := parsed.Text
	hasHTML := strings.TrimSpace(parsed.HTML) != ""
	attachments := make([]string, 0, len(parsed.Files))
	if parsed.IsEncrypted {
		body = ""
		hasHTML = false
	} else {
		for _, attachment := range parsed.Files {
			if contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType)); contentType != "" {
				attachments = append(attachments, contentType)
			}
		}
	}
	mimeType := "text/plain"
	if hasHTML {
		mimeType = "text/html"
	}
	return bootstrapParsedMessage{
		Model: spammodel.Message{
			Subject: subject, Body: boundedText(body, maxBodyBytes), From: from,
			To:       recipientAddresses(to, firstBootstrapValue(parsed.CC, envelope.CC)),
			MIMEType: mimeType, AttachmentTypes: attachments, HTML: hasHTML,
		},
		MessageID: messageID, From: from, To: to, Subject: subject,
		IsEncrypted: parsed.IsEncrypted,
	}, nil
}

func firstBootstrapValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
