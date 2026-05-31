// File overview: SMTP forwarding helpers used by backend plugin actions.

package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"rolltop/backend/mailparse"
	"rolltop/backend/plugins"
	"rolltop/backend/smtpclient"
	"rolltop/backend/store"
)

// ForwardMessage sends a simple inline forward of one stored message through
// the user's configured SMTP identity. It intentionally does not append to Sent;
// filter audit rows are the durable record for automatic forwards.
func (s *Service) ForwardMessage(ctx context.Context, userID, messageID int64, to string, headers []plugins.MailHeader) error {
	if s == nil || s.Sender == nil {
		return errors.New("SMTP sending is not configured")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return errors.New("forward recipient is required")
	}
	if _, err := mail.ParseAddressList(to); err != nil {
		return fmt.Errorf("forward recipient is invalid: %w", err)
	}
	msg, err := s.Store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return err
	}
	raw, err := s.FetchRawMessageForMessage(ctx, userID, msg)
	if err != nil {
		return err
	}
	for _, header := range headers {
		if strings.EqualFold(strings.TrimSpace(header.Name), "X-Rolltop-Forwarded-By") && messageHasHeaderValue(raw, header.Name, header.Value) {
			return fmt.Errorf("message was already forwarded by this Rolltop account")
		}
	}
	identity, smtpAccount, err := s.forwardIdentity(ctx, userID, msg.AccountID)
	if err != nil {
		return err
	}
	parsed, parseErr := mailparse.Parse(raw)
	bodyText := msg.BodyText
	bodyHTML := msg.BodyHTML
	if parseErr == nil {
		bodyText = parsed.Text
		bodyHTML = parsed.HTML
	}
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") && !strings.HasPrefix(strings.ToLower(subject), "fw:") {
		subject = "Fwd: " + subject
	}
	out := smtpclient.Message{
		From:         mailAddressHeader(identity.DisplayName, identity.Email),
		To:           []string{to},
		Subject:      subject,
		BodyText:     forwardedText(msg, bodyText),
		BodyHTML:     forwardedHTML(msg, bodyHTML),
		MessageID:    smtpclient.NewMessageID(identity.Email),
		Date:         time.Now(),
		ExtraHeaders: mailHeaders(headers),
	}
	envelope := store.MailAccount{
		UserID:                smtpAccount.UserID,
		Email:                 identity.Email,
		SMTPHost:              smtpAccount.Host,
		SMTPPort:              smtpAccount.Port,
		SMTPUsername:          smtpAccount.Username,
		EncryptedSMTPPassword: smtpAccount.EncryptedPassword,
		SMTPUseTLS:            smtpAccount.UseTLS,
	}
	_, err = s.Sender.Send(ctx, envelope, out)
	return err
}

func (s *Service) forwardIdentity(ctx context.Context, userID, accountID int64) (store.MailIdentity, store.SMTPAccount, error) {
	identities, err := s.Store.ListMailIdentitiesForUser(ctx, userID)
	if err != nil {
		return store.MailIdentity{}, store.SMTPAccount{}, err
	}
	for _, preferred := range []bool{true, false} {
		for _, identity := range identities {
			if identity.SMTPAccountID <= 0 || strings.TrimSpace(identity.Email) == "" {
				continue
			}
			if preferred && identity.IMAPAccountID != accountID {
				continue
			}
			smtpAccount, err := s.Store.GetSMTPAccountForUser(ctx, userID, identity.SMTPAccountID)
			if err == nil {
				return identity, smtpAccount, nil
			}
		}
	}
	return store.MailIdentity{}, store.SMTPAccount{}, errors.New("no SMTP identity is configured for automatic forwarding")
}

func messageHasHeaderValue(raw []byte, name, value string) bool {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	value = strings.TrimSpace(value)
	for _, existing := range msg.Header[textproto.CanonicalMIMEHeaderKey(name)] {
		if strings.TrimSpace(existing) == value {
			return true
		}
	}
	return false
}

func mailHeaders(headers []plugins.MailHeader) []smtpclient.Header {
	out := make([]smtpclient.Header, 0, len(headers))
	for _, header := range headers {
		name := strings.TrimSpace(header.Name)
		value := strings.TrimSpace(header.Value)
		if name == "" || value == "" {
			continue
		}
		out = append(out, smtpclient.Header{Name: name, Value: value})
	}
	return out
}

func mailAddressHeader(label, email string) string {
	email = strings.TrimSpace(email)
	label = strings.TrimSpace(label)
	if email == "" {
		return ""
	}
	if label == "" || strings.EqualFold(label, email) {
		return email
	}
	return (&mail.Address{Name: label, Address: email}).String()
}

func forwardedText(msg store.MessageRecord, body string) string {
	var b strings.Builder
	b.WriteString("\n\n---------- Forwarded message ---------\n")
	if strings.TrimSpace(msg.FromAddr) != "" {
		b.WriteString("From: ")
		b.WriteString(msg.FromAddr)
		b.WriteString("\n")
	}
	if !msg.Date.IsZero() {
		b.WriteString("Date: ")
		b.WriteString(msg.Date.Local().Format("Mon, Jan 2, 2006 at 3:04 PM"))
		b.WriteString("\n")
	}
	if strings.TrimSpace(msg.Subject) != "" {
		b.WriteString("Subject: ")
		b.WriteString(msg.Subject)
		b.WriteString("\n")
	}
	if strings.TrimSpace(msg.ToAddr) != "" {
		b.WriteString("To: ")
		b.WriteString(msg.ToAddr)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(body))
	return b.String()
}

func forwardedHTML(msg store.MessageRecord, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<br><br><div>---------- Forwarded message ---------</div>`)
	if strings.TrimSpace(msg.FromAddr) != "" {
		b.WriteString(`<div><strong>From:</strong> `)
		b.WriteString(html.EscapeString(msg.FromAddr))
		b.WriteString(`</div>`)
	}
	if !msg.Date.IsZero() {
		b.WriteString(`<div><strong>Date:</strong> `)
		b.WriteString(html.EscapeString(msg.Date.Local().Format("Mon, Jan 2, 2006 at 3:04 PM")))
		b.WriteString(`</div>`)
	}
	if strings.TrimSpace(msg.Subject) != "" {
		b.WriteString(`<div><strong>Subject:</strong> `)
		b.WriteString(html.EscapeString(msg.Subject))
		b.WriteString(`</div>`)
	}
	if strings.TrimSpace(msg.ToAddr) != "" {
		b.WriteString(`<div><strong>To:</strong> `)
		b.WriteString(html.EscapeString(msg.ToAddr))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<br><div>`)
	b.WriteString(body)
	b.WriteString(`</div>`)
	return b.String()
}
