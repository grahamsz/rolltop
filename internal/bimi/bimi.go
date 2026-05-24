package bimi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

const (
	positiveTTL = 30 * 24 * time.Hour
	negativeTTL = 7 * 24 * time.Hour
	maxSVGBytes = 256 * 1024
)

type Result struct {
	Domain    string
	LogoURL   string
	SVG       string
	Status    string
	Error     string
	FetchedAt time.Time
	ExpiresAt time.Time
}

type Resolver struct {
	DNS        *net.Resolver
	HTTPClient *http.Client
	Now        func() time.Time
}

func DomainFromAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if addrs, err := mail.ParseAddressList(value); err == nil && len(addrs) > 0 {
		return NormalizeDomain(domainFromEmail(addrs[0].Address))
	}
	if idx := strings.LastIndex(value, "@"); idx >= 0 {
		return NormalizeDomain(domainFromEmail(value[idx+1:]))
	}
	return ""
}

func NormalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Trim(value, "<>.,;:()[]{}\"'")
	if value == "" || len(value) > 253 {
		return ""
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 63 {
			return ""
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return ""
		}
		if strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return ""
		}
	}
	return value
}

func (r Resolver) Fetch(ctx context.Context, domain string) Result {
	now := r.now()
	result := Result{
		Domain:    NormalizeDomain(domain),
		Status:    "missing",
		FetchedAt: now,
		ExpiresAt: now.Add(negativeTTL),
	}
	if result.Domain == "" {
		result.Status = "invalid"
		result.Error = "invalid domain"
		return result
	}
	logoURL, err := r.lookupLogoURL(ctx, result.Domain)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.LogoURL = logoURL
	svg, err := r.fetchSVG(ctx, logoURL)
	if err != nil {
		result.Status = "invalid"
		result.Error = err.Error()
		return result
	}
	result.SVG = svg
	result.Status = "ok"
	result.Error = ""
	result.ExpiresAt = now.Add(positiveTTL)
	return result
}

func (r Resolver) lookupLogoURL(ctx context.Context, domain string) (string, error) {
	resolver := r.DNS
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	records, err := resolver.LookupTXT(ctx, "default._bimi."+domain)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		fields := parseBIMITXT(record)
		if !strings.EqualFold(fields["v"], "BIMI1") {
			continue
		}
		logoURL := strings.TrimSpace(fields["l"])
		if logoURL == "" {
			return "", errors.New("BIMI record has no logo URL")
		}
		if err := validateLogoURL(ctx, resolver, logoURL); err != nil {
			return "", err
		}
		return logoURL, nil
	}
	return "", errors.New("BIMI record not found")
}

func (r Resolver) fetchSVG(ctx context.Context, rawURL string) (string, error) {
	resolver := r.DNS
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	originalCheck := client.CheckRedirect
	clientCopy := *client
	clientCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if originalCheck != nil {
			if err := originalCheck(req, via); err != nil {
				return err
			}
		}
		if len(via) >= 5 {
			return errors.New("too many BIMI logo redirects")
		}
		return validateLogoURL(req.Context(), resolver, req.URL.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "image/svg+xml, image/*;q=0.5")
	req.Header.Set("User-Agent", "MailMirror/1.0 BIMI")
	res, err := clientCopy.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("fetch BIMI logo: HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxSVGBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxSVGBytes {
		return "", errors.New("BIMI logo is too large")
	}
	svg := strings.TrimSpace(string(body))
	if err := validateSVG(svg); err != nil {
		return "", err
	}
	return svg, nil
}

func (r Resolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func parseBIMITXT(record string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(record, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return out
}

func validateLogoURL(ctx context.Context, resolver *net.Resolver, rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return err
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return errors.New("BIMI logo URL must use HTTPS")
	}
	if u.User != nil {
		return errors.New("BIMI logo URL must not include credentials")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return errors.New("BIMI logo URL has no host")
	}
	lowerHost := strings.ToLower(host)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") {
		return errors.New("BIMI logo URL host is not allowed")
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("BIMI logo URL host has no addresses")
	}
	for _, ip := range ips {
		if !publicIP(ip.IP) {
			return errors.New("BIMI logo URL resolves to a private address")
		}
	}
	return nil
}

func validateSVG(svg string) error {
	lower := strings.ToLower(svg)
	if !strings.Contains(lower, "<svg") {
		return errors.New("BIMI logo is not SVG")
	}
	blocked := []string{"<script", "<foreignobject", "<?xml-stylesheet", " onload=", " onerror=", " javascript:"}
	for _, token := range blocked {
		if strings.Contains(lower, token) {
			return errors.New("BIMI logo SVG contains unsupported active content")
		}
	}
	return nil
}

func publicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else {
		ip = ip.To16()
	}
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsUnspecified() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsMulticast()
}

func domainFromEmail(value string) string {
	value = strings.TrimSpace(value)
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	return value
}
