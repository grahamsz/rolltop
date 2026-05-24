package smtpclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
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

	mmcrypto "mailmirror/internal/crypto"
	"mailmirror/internal/store"
)

type Message struct {
	From       string
	To         []string
	Cc         []string
	Bcc        []string
	Subject    string
	BodyText   string
	BodyHTML   string
	MessageID  string
	InReplyTo  string
	References string
	Date       time.Time
}

type Sender struct {
	MasterKey []byte
	Timeout   time.Duration
}

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

func BuildRaw(msg Message) ([]byte, []string, error) {
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
	if len(recipients) == 0 {
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
	if strings.TrimSpace(msg.BodyHTML) != "" {
		boundary := "mailmirror-alt-" + strings.Trim(msg.MessageID, "<>")
		boundary = strings.NewReplacer("@", "-", ".", "-", "_", "-").Replace(boundary)
		writeHeader(w, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		_, _ = w.WriteString("\r\n")
		writePart(w, boundary, `text/plain; charset="utf-8"`, msg.BodyText)
		writePart(w, boundary, `text/html; charset="utf-8"`, msg.BodyHTML)
		_, _ = fmt.Fprintf(w, "--%s--\r\n", boundary)
	} else {
		writeHeader(w, "Content-Type", `text/plain; charset="utf-8"`)
		writeHeader(w, "Content-Transfer-Encoding", "8bit")
		_, _ = w.WriteString("\r\n")
		body := normalizeCRLF(msg.BodyText)
		_, _ = w.WriteString(body)
		if !strings.HasSuffix(body, "\r\n") {
			_, _ = w.WriteString("\r\n")
		}
	}
	if err := w.Flush(); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), recipients, nil
}

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
