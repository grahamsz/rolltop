package maildiscovery

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 256 * 1024
const maxRedirects = 3

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func publicIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func discoveryURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !validHost(u.Hostname()) || !strings.Contains(u.Hostname(), ".") || (u.Port() != "" && u.Port() != "443") {
		return nil, errors.New("invalid discovery URL")
	}
	return u, nil
}

func safeDial(ctx context.Context, r resolver, dial func(context.Context, string, string) (net.Conn, error), network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errors.New("invalid discovery address")
	}
	ips, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			continue
		}
		// Dial the checked IP itself to prevent DNS rebinding; HTTP retains the
		// original hostname for TLS certificate verification and SNI.
		conn, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("discovery host is unavailable")
}

func newHTTPClient(r resolver) *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return safeDial(ctx, r, dialer.DialContext, network, address)
			},
		},
		// Redirects are handled explicitly so POST retains its XML body and
		// every HTTP/XML redirect shares the same hop budget.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (d *Discoverer) fetch(ctx context.Context, target, body string) ([]byte, string, error) {
	u, err := discoveryURL(target)
	if err != nil {
		return nil, "", err
	}
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/xml, text/xml")
	if body != "" {
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 301, 302, 303, 307, 308:
		location, err := resp.Location()
		if err != nil {
			return nil, "", err
		}
		if _, err := discoveryURL(location.String()); err != nil {
			return nil, "", err
		}
		return nil, location.String(), nil
	case 200:
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil || len(data) > maxResponseBytes {
			return nil, "", errors.New("invalid discovery response")
		}
		return data, "", nil
	default:
		return nil, "", errors.New("discovery endpoint returned no configuration")
	}
}
