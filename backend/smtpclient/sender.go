// File overview: SMTP send implementation for composed messages.

package smtpclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	mmcrypto "mailmirror/backend/crypto"
	"mailmirror/backend/store"
)

// Attachment is an outgoing MIME attachment or inline part prepared by compose.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Inline      bool
	Data        []byte
}

// Message is the normalized outbound compose payload passed to the SMTP sender.
type Message struct {
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	Subject     string
	BodyText    string
	BodyHTML    string
	MessageID   string
	InReplyTo   string
	References  string
	Date        time.Time
	Attachments []Attachment
}

// Sender sends compose messages through an encrypted MailMirror SMTP account.
type Sender struct {
	MasterKey []byte
	Timeout   time.Duration
}

// Send builds a MIME message from the compose form and sends it through the configured SMTP account.
func (s *Sender) Send(ctx context.Context, account store.MailAccount, msg Message) ([]byte, error) {
	raw, recipients, err := BuildRaw(msg)
	if err != nil {
		return nil, err
	}
	if err := s.SendRaw(ctx, account, recipients, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// SendRaw sends an already-built RFC822 payload to all recipients using the configured SMTP account.
func (s *Sender) SendRaw(ctx context.Context, account store.MailAccount, recipients []string, raw []byte) error {
	if len(recipients) == 0 {
		return errors.New("message has no recipients")
	}
	password, err := mmcrypto.DecryptString(s.MasterKey, account.EncryptedSMTPPassword)
	if err != nil {
		return fmt.Errorf("decrypt SMTP password: %w", err)
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	addr := net.JoinHostPort(account.SMTPHost, fmt.Sprintf("%d", account.SMTPPort))
	dialer := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	if account.SMTPUseTLS && account.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: account.SMTPHost, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to SMTP server %s: %w", addr, err)
	}
	defer conn.Close()

	c, err := smtp.NewClient(conn, account.SMTPHost)
	if err != nil {
		return fmt.Errorf("initialize SMTP client for %s: %w", addr, err)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("SMTP hello: %w", err)
	}
	if account.SMTPUseTLS && account.SMTPPort != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: account.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("start SMTP TLS: %w", err)
			}
		} else {
			return errors.New("SMTP server does not advertise STARTTLS")
		}
	}
	if strings.TrimSpace(account.SMTPUsername) != "" {
		auth := smtp.PlainAuth("", account.SMTPUsername, password, account.SMTPHost)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	fromAddr, err := firstAddress(account.Email)
	if err != nil {
		return err
	}
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, recipient := range recipients {
		if err := c.Rcpt(recipient); err != nil {
			return fmt.Errorf("SMTP RCPT TO: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := io.Copy(w, bytes.NewReader(raw)); err != nil {
		_ = w.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	return c.Quit()
}

// BuildRaw constructs the RFC822 message, including text/html alternatives,
// headers, and attachments. It is used for real sends, so at least one
// recipient is required before SMTP is attempted.
func BuildRaw(msg Message) ([]byte, []string, error) {
	return buildRaw(msg, true)
}

// BuildDraftRaw constructs an unsent RFC822 draft. Drafts are allowed to be
// incomplete, so To/Cc/Bcc may all be empty while the MIME body and attachments
// are still preserved for IMAP APPEND.
func BuildDraftRaw(msg Message) ([]byte, error) {
	raw, _, err := buildRaw(msg, false)
	return raw, err
}

func buildRaw(msg Message, requireRecipients bool) ([]byte, []string, error) {
	if msg.Date.IsZero() {
		msg.Date = time.Now()
	}
	from, err := mail.ParseAddress(msg.From)
	if err != nil {
		return nil, nil, fmt.Errorf("from address: %w", err)
	}
	to, err := parseAddresses(msg.To)
	if err != nil {
		return nil, nil, fmt.Errorf("to address: %w", err)
	}
	cc, err := parseAddresses(msg.Cc)
	if err != nil {
		return nil, nil, fmt.Errorf("cc address: %w", err)
	}
	bcc, err := parseAddresses(msg.Bcc)
	if err != nil {
		return nil, nil, fmt.Errorf("bcc address: %w", err)
	}
	recipients := addressStrings(append(append(to, cc...), bcc...))
	if requireRecipients && len(recipients) == 0 {
		return nil, nil, errors.New("message has no recipients")
	}
	if strings.TrimSpace(msg.MessageID) == "" {
		msg.MessageID = NewMessageID(from.Address)
	}

	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	writeHeader(w, "From", from.String())
	if len(to) > 0 {
		writeHeader(w, "To", addressListString(to))
	}
	if len(cc) > 0 {
		writeHeader(w, "Cc", addressListString(cc))
	}
	if !requireRecipients && len(bcc) > 0 {
		writeHeader(w, "Bcc", addressListString(bcc))
	}
	writeHeader(w, "Subject", mime.QEncoding.Encode("utf-8", strings.TrimSpace(msg.Subject)))
	writeHeader(w, "Date", msg.Date.Format(time.RFC1123Z))
	writeHeader(w, "Message-ID", msg.MessageID)
	if strings.TrimSpace(msg.InReplyTo) != "" {
		writeHeader(w, "In-Reply-To", sanitizeHeaderValue(msg.InReplyTo))
	}
	if strings.TrimSpace(msg.References) != "" {
		writeHeader(w, "References", sanitizeHeaderValue(msg.References))
	}
	writeHeader(w, "MIME-Version", "1.0")
	writeRootBody(w, msg)
	if err := w.Flush(); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), recipients, nil
}

func writeRootBody(w *bufio.Writer, msg Message) {
	inlineAttachments, regularAttachments := splitAttachments(msg.Attachments)
	hasInlineHTML := len(inlineAttachments) > 0 && strings.TrimSpace(msg.BodyHTML) != ""
	if len(msg.Attachments) == 0 {
		writeBodyEntity(w, msg)
		return
	}
	if len(regularAttachments) > 0 {
		boundary := boundaryFor(msg, "mixed")
		writeHeader(w, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": boundary}))
		_, _ = w.WriteString("\r\n")
		if hasInlineHTML {
			relatedBoundary := boundaryFor(msg, "related")
			_, _ = fmt.Fprintf(w, "--%s\r\n", boundary)
			writeHeader(w, "Content-Type", mime.FormatMediaType("multipart/related", map[string]string{"boundary": relatedBoundary}))
			_, _ = w.WriteString("\r\n")
			writeBodyEntityPart(w, relatedBoundary, msg)
			for _, attachment := range inlineAttachments {
				writeAttachmentPart(w, relatedBoundary, attachment)
			}
			_, _ = fmt.Fprintf(w, "--%s--\r\n", relatedBoundary)
		} else {
			writeBodyEntityPart(w, boundary, msg)
			regularAttachments = append(inlineAttachments, regularAttachments...)
		}
		for _, attachment := range regularAttachments {
			writeAttachmentPart(w, boundary, attachment)
		}
		_, _ = fmt.Fprintf(w, "--%s--\r\n", boundary)
		return
	}
	if hasInlineHTML {
		boundary := boundaryFor(msg, "related")
		writeHeader(w, "Content-Type", mime.FormatMediaType("multipart/related", map[string]string{"boundary": boundary}))
		_, _ = w.WriteString("\r\n")
		writeBodyEntityPart(w, boundary, msg)
		for _, attachment := range inlineAttachments {
			writeAttachmentPart(w, boundary, attachment)
		}
		_, _ = fmt.Fprintf(w, "--%s--\r\n", boundary)
		return
	}
	boundary := boundaryFor(msg, "mixed")
	writeHeader(w, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": boundary}))
	_, _ = w.WriteString("\r\n")
	writeBodyEntityPart(w, boundary, msg)
	for _, attachment := range inlineAttachments {
		writeAttachmentPart(w, boundary, attachment)
	}
	_, _ = fmt.Fprintf(w, "--%s--\r\n", boundary)
}

func splitAttachments(attachments []Attachment) ([]Attachment, []Attachment) {
	var inlineAttachments []Attachment
	var regularAttachments []Attachment
	for _, attachment := range attachments {
		if attachment.Inline {
			inlineAttachments = append(inlineAttachments, attachment)
		} else {
			regularAttachments = append(regularAttachments, attachment)
		}
	}
	return inlineAttachments, regularAttachments
}

func writeBodyEntityPart(w *bufio.Writer, boundary string, msg Message) {
	_, _ = fmt.Fprintf(w, "--%s\r\n", boundary)
	writeBodyEntity(w, msg)
}

func writeBodyEntity(w *bufio.Writer, msg Message) {
	if strings.TrimSpace(msg.BodyHTML) != "" {
		boundary := boundaryFor(msg, "alt")
		writeHeader(w, "Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": boundary}))
		_, _ = w.WriteString("\r\n")
		writePart(w, boundary, `text/plain; charset="utf-8"`, msg.BodyText)
		writePart(w, boundary, `text/html; charset="utf-8"`, msg.BodyHTML)
		_, _ = fmt.Fprintf(w, "--%s--\r\n", boundary)
		return
	}
	writeHeader(w, "Content-Type", `text/plain; charset="utf-8"`)
	writeHeader(w, "Content-Transfer-Encoding", "8bit")
	_, _ = w.WriteString("\r\n")
	body := normalizeCRLF(msg.BodyText)
	_, _ = w.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		_, _ = w.WriteString("\r\n")
	}
}

func writeAttachmentPart(w *bufio.Writer, boundary string, attachment Attachment) {
	_, _ = fmt.Fprintf(w, "--%s\r\n", boundary)
	filename := sanitizeAttachmentFilename(attachment.Filename)
	contentType := strings.TrimSpace(attachment.ContentType)
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || strings.TrimSpace(mediaType) == "" {
		mediaType = "application/octet-stream"
		params = map[string]string{}
	}
	if filename != "" {
		params["name"] = filename
	}
	writeHeader(w, "Content-Type", mime.FormatMediaType(mediaType, params))
	writeHeader(w, "Content-Transfer-Encoding", "base64")
	if attachment.Inline && strings.TrimSpace(attachment.ContentID) != "" {
		writeHeader(w, "Content-ID", contentIDHeader(attachment.ContentID))
	}
	disposition := "attachment"
	if attachment.Inline {
		disposition = "inline"
	}
	dispositionParams := map[string]string{}
	if filename != "" {
		dispositionParams["filename"] = filename
	}
	writeHeader(w, "Content-Disposition", mime.FormatMediaType(disposition, dispositionParams))
	_, _ = w.WriteString("\r\n")
	writeBase64Body(w, attachment.Data)
}

func writeBase64Body(w *bufio.Writer, data []byte) {
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(data)))
	base64.StdEncoding.Encode(encoded, data)
	for len(encoded) > 0 {
		lineLength := 76
		if len(encoded) < lineLength {
			lineLength = len(encoded)
		}
		_, _ = w.Write(encoded[:lineLength])
		_, _ = w.WriteString("\r\n")
		encoded = encoded[lineLength:]
	}
}

func boundaryFor(msg Message, kind string) string {
	boundary := "mailmirror-" + kind + "-" + strings.Trim(msg.MessageID, "<>")
	return strings.NewReplacer("@", "-", ".", "-", "_", "-", "/", "-", "+", "-").Replace(boundary)
}

func contentIDHeader(contentID string) string {
	contentID = strings.Trim(strings.TrimSpace(contentID), "<>")
	return "<" + sanitizeHeaderValue(contentID) + ">"
}

func sanitizeAttachmentFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	filename = strings.ReplaceAll(filename, "\x00", "")
	filename = strings.ReplaceAll(filename, "/", "_")
	filename = strings.ReplaceAll(filename, "\\", "_")
	if filename == "" {
		return "attachment"
	}
	return filename
}

// NewMessageID creates a local Message-ID suitable for outbound composed mail.
func NewMessageID(fromAddress string) string {
	domain := "mailmirror.local"
	if _, host, ok := strings.Cut(fromAddress, "@"); ok && strings.TrimSpace(host) != "" {
		domain = strings.ToLower(strings.TrimSpace(host))
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("<%d@mailmirror.%s>", time.Now().UnixNano(), domain)
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(random), domain)
}

func writePart(w *bufio.Writer, boundary, contentType, body string) {
	_, _ = fmt.Fprintf(w, "--%s\r\n", boundary)
	writeHeader(w, "Content-Type", contentType)
	writeHeader(w, "Content-Transfer-Encoding", "8bit")
	_, _ = w.WriteString("\r\n")
	body = normalizeCRLF(body)
	_, _ = w.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		_, _ = w.WriteString("\r\n")
	}
}

func normalizeCRLF(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}

func parseAddresses(values []string) ([]*mail.Address, error) {
	var out []*mail.Address
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		addrs, err := mail.ParseAddressList(value)
		if err != nil {
			return nil, err
		}
		out = append(out, addrs...)
	}
	return out, nil
}

func addressStrings(addrs []*mail.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.Address)
	}
	return out
}

func addressListString(addrs []*mail.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		parts = append(parts, addr.String())
	}
	return strings.Join(parts, ", ")
}

func writeHeader(w *bufio.Writer, name, value string) {
	_, _ = fmt.Fprintf(w, "%s: %s\r\n", name, sanitizeHeaderValue(value))
}

func sanitizeHeaderValue(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func firstAddress(value string) (string, error) {
	addr, err := mail.ParseAddress(value)
	if err != nil {
		return "", fmt.Errorf("from address: %w", err)
	}
	return addr.Address, nil
}
