package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPFromRequest(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("2001:db8:1::/48"),
	}
	for _, tc := range []struct {
		name      string
		remote    string
		forwarded []string
		realIP    []string
		noTrust   bool
		want      string
	}{
		{name: "IPv4 peer", remote: "192.0.2.1:1234", want: "192.0.2.1"},
		{name: "IPv6 peer", remote: "[2001:0db8::1]:1234", want: "2001:db8::1"},
		{name: "peer without port", remote: "2001:db8::1", want: "2001:db8::1"},
		{name: "mapped peer", remote: "[::ffff:192.0.2.1]:1234", want: "192.0.2.1"},
		{name: "scoped peer", remote: "[fe80::1%eth0]:1234", want: "fe80::1"},
		{name: "invalid peer", remote: "invalid\nsecurity login_failed ip=192.0.2.5", forwarded: []string{"192.0.2.3"}, want: "unknown"},
		{name: "empty peer", want: "unknown"},
		{name: "no trusted proxies", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3"}, realIP: []string{"192.0.2.4"}, noTrust: true, want: "10.0.0.1"},
		{name: "untrusted peer", remote: "198.51.100.1:1234", forwarded: []string{"192.0.2.3, 10.0.0.2"}, realIP: []string{"192.0.2.4"}, want: "198.51.100.1"},
		{name: "trusted peer without headers", remote: "10.0.0.1:1234", want: "10.0.0.1"},
		{name: "forwarded client", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3"}, want: "192.0.2.3"},
		{name: "IPv6 proxy and client", remote: "[2001:db8:1::1]:1234", forwarded: []string{"2001:0db8:2::3"}, want: "2001:db8:2::3"},
		{name: "mapped proxy and client", remote: "[::ffff:10.0.0.1]:1234", forwarded: []string{"::ffff:192.0.2.3"}, want: "192.0.2.3"},
		{name: "trusted chain", remote: "10.0.0.1:1234", forwarded: []string{" 192.0.2.3 , 10.0.0.2 , 2001:db8:1::1 "}, want: "192.0.2.3"},
		{name: "repeated header lines", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3", "10.0.0.2"}, want: "192.0.2.3"},
		{name: "spoofed leftmost IP", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.99, 198.51.100.1, 10.0.0.2"}, want: "198.51.100.1"},
		{name: "untrusted prefix ignored", remote: "10.0.0.1:1234", forwarded: []string{"garbage, 198.51.100.1"}, want: "198.51.100.1"},
		{name: "client in trusted range", remote: "10.0.0.1:1234", forwarded: []string{"10.0.0.3, 10.0.0.2"}, want: "10.0.0.3"},
		{name: "XFF takes precedence", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3"}, realIP: []string{"192.0.2.4"}, want: "192.0.2.3"},
		{name: "malformed XFF cannot fall back to real IP", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3, garbage, 10.0.0.2"}, realIP: []string{"192.0.2.4"}, want: "10.0.0.1"},
		{name: "empty XFF", remote: "10.0.0.1:1234", forwarded: []string{""}, realIP: []string{"192.0.2.4"}, want: "10.0.0.1"},
		{name: "empty hop", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3,"}, want: "10.0.0.1"},
		{name: "XFF port rejected", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3:8080"}, want: "10.0.0.1"},
		{name: "XFF zone rejected", remote: "10.0.0.1:1234", forwarded: []string{"fe80::1%eth0"}, want: "10.0.0.1"},
		{name: "XFF log injection rejected", remote: "10.0.0.1:1234", forwarded: []string{"192.0.2.3\nsecurity login_failed ip=192.0.2.99"}, want: "10.0.0.1"},
		{name: "real IP", remote: "10.0.0.1:1234", realIP: []string{" 192.0.2.4 "}, want: "192.0.2.4"},
		{name: "real IPv6", remote: "10.0.0.1:1234", realIP: []string{"2001:0db8:2::3"}, want: "2001:db8:2::3"},
		{name: "real IP list rejected", remote: "10.0.0.1:1234", realIP: []string{"192.0.2.3, 192.0.2.4"}, want: "10.0.0.1"},
		{name: "repeated real IP rejected", remote: "10.0.0.1:1234", realIP: []string{"192.0.2.3", "192.0.2.4"}, want: "10.0.0.1"},
		{name: "real IP log injection rejected", remote: "10.0.0.1:1234", realIP: []string{"192.0.2.3\r\nforged"}, want: "10.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{trustedProxies: trusted}
			if tc.noTrust {
				s.trustedProxies = nil
			}
			r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
			r.RemoteAddr = tc.remote
			for _, value := range tc.forwarded {
				r.Header.Add("X-Forwarded-For", value)
			}
			for _, value := range tc.realIP {
				r.Header.Add("X-Real-IP", value)
			}
			if got := s.clientIPFromRequest(r); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
}
