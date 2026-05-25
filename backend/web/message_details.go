// File overview: Message-detail assembly, headers, body parts, attachments, and display metadata.

package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	oneclickunsubscribe "mailmirror/backend/plugins/one_click_unsubscribe"
	"mailmirror/backend/store"
)

var errOneClickUnavailable = errors.New("one-click unsubscribe unavailable")

const oneClickUnsubscribeRecentWindow = 7 * 24 * time.Hour

type messageHeaderDetail struct {
	Label string
	Value string
}

func (s *Server) messageHeaderDetails(ctx context.Context, userID int64, msg store.MessageRecord) []messageHeaderDetail {
	header := s.rawMessageHeader(ctx, userID, msg)
	var details []messageHeaderDetail
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		details = append(details, messageHeaderDetail{Label: label, Value: value})
	}
	add("from", msg.FromAddr)
	add("reply-to", decodedAddressHeader(header.Get("Reply-To")))
	add("to", msg.ToAddr)
	add("cc", msg.CCAddr)
	if !msg.Date.IsZero() {
		add("date", msg.Date.Local().Format("02 Jan 2006, 15:04"))
	} else if rawDate := strings.TrimSpace(header.Get("Date")); rawDate != "" {
		if parsed, err := mail.ParseDate(rawDate); err == nil {
			add("date", parsed.Local().Format("02 Jan 2006, 15:04"))
		} else {
			add("date", rawDate)
		}
	}
	add("subject", msg.Subject)
	add("mailed-by", mailedByDomain(header, msg.FromAddr))
	add("signed-by", signedByDomain(header.Get("DKIM-Signature")))
	add("message-id", msg.MessageIDHeader)
	return details
}

func (s *Server) hasOneClickUnsubscribe(ctx context.Context, userID int64, msg store.MessageRecord) bool {
	_, ok := s.oneClickUnsubscribeTarget(ctx, userID, msg)
	return ok
}

func (s *Server) oneClickUnsubscribeTarget(ctx context.Context, userID int64, msg store.MessageRecord) (*url.URL, bool) {
	target, ok := oneClickUnsubscribeURL(s.rawMessageHeader(ctx, userID, msg))
	if !ok {
		return nil, false
	}
	return target, true
}

func (s *Server) recentOneClickUnsubscribeSentAt(ctx context.Context, userID int64, msg store.MessageRecord, target string) time.Time {
	userDB, err := s.store.UserDB(ctx, userID)
	if err != nil {
		return time.Time{}
	}
	send, err := oneclickunsubscribe.LatestSend(ctx, userDB, userID, msg.ID, target, time.Now().Add(-oneClickUnsubscribeRecentWindow))
	if err != nil {
		return time.Time{}
	}
	return send.SentAt
}

func (s *Server) performOneClickUnsubscribe(ctx context.Context, target *url.URL) error {
	if target == nil {
		return errOneClickUnavailable
	}
	if err := validateOutboundHTTPS(ctx, target); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/plain, text/html, */*;q=0.8")
	client := &http.Client{
		Timeout: 12 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			if req.URL == nil || req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			return validateOutboundHTTPS(req.Context(), req.URL)
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("unsubscribe endpoint returned non-2xx")
	}
	return nil
}

func (s *Server) rawMessageHeader(ctx context.Context, userID int64, msg store.MessageRecord) mail.Header {
	raw, err := s.rawMessageBytes(ctx, userID, msg)
	if err != nil {
		return mail.Header{}
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return mail.Header{}
	}
	return parsed.Header
}

func oneClickUnsubscribeURL(header mail.Header) (*url.URL, bool) {
	post := strings.ToLower(header.Get("List-Unsubscribe-Post"))
	if !strings.Contains(post, "list-unsubscribe=one-click") {
		return nil, false
	}
	for _, candidate := range listUnsubscribeCandidates(header.Get("List-Unsubscribe")) {
		u, err := url.Parse(candidate)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			continue
		}
		return u, true
	}
	return nil, false
}

func listUnsubscribeCandidates(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "<") {
			if end := strings.Index(part, ">"); end > 1 {
				part = part[1:end]
			}
		}
		part = strings.Trim(part, "<> \t\r\n")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func validateOutboundHTTPS(ctx context.Context, target *url.URL) error {
	if target == nil || target.Scheme != "https" || target.Hostname() == "" {
		return errors.New("unsubscribe URL must be HTTPS")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, target.Hostname())
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("unsubscribe host did not resolve")
	}
	for _, addr := range ips {
		ip := addr.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return errors.New("unsubscribe host resolves to a private address")
		}
	}
	return nil
}

func decodedAddressHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if decoded, err := (&mime.WordDecoder{}).DecodeHeader(value); err == nil {
		value = decoded
	}
	if addrs, err := mail.ParseAddressList(value); err == nil {
		out := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			out = append(out, addr.String())
		}
		return strings.Join(out, ", ")
	}
	return value
}

func mailedByDomain(header mail.Header, from string) string {
	for _, key := range []string{"X-Mailer-Domain", "X-Sender-Domain"} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	if returnPath := strings.TrimSpace(header.Get("Return-Path")); returnPath != "" {
		if addr, err := mail.ParseAddress(strings.Trim(returnPath, "<>")); err == nil {
			if domain := domainFromAddress(addr.Address); domain != "" {
				return domain
			}
		}
	}
	return domainFromAddress(senderEmail(from))
}

func signedByDomain(value string) string {
	for _, part := range strings.Split(value, ";") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "d") {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func domainFromAddress(value string) string {
	value = strings.TrimSpace(value)
	if at := strings.LastIndex(value, "@"); at >= 0 && at+1 < len(value) {
		return strings.Trim(value[at+1:], "<> ")
	}
	return ""
}
