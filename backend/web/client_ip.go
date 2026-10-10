// File overview: Client IP resolution through explicitly trusted reverse proxies.

package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientIPFromRequest returns a canonical IP for throttling and security logs.
// Forwarding headers have authority only when the socket peer is trusted.
func (s *Server) clientIPFromRequest(r *http.Request) string {
	host := r.RemoteAddr
	if addr, _, err := net.SplitHostPort(host); err == nil {
		host = addr
	}
	peer, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		// Never put an unvalidated address into a security log line.
		return "unknown"
	}
	peer = peer.WithZone("").Unmap()
	if !s.isTrustedProxy(peer) {
		return peer.String()
	}
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		// Each proxy appends its observed peer. Discard only trusted hops
		// from the right; anything left of the first untrusted hop is supplied
		// by that client and cannot be used to identify it.
		hops := strings.Split(strings.Join(values, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			addr, ok := forwardedIP(hops[i])
			if !ok {
				return peer.String()
			}
			if !s.isTrustedProxy(addr) || i == 0 {
				return addr.String()
			}
		}
		return peer.String()
	}
	// X-Real-IP is a single address and must be overwritten by the trusted
	// proxy. Only use it when X-Forwarded-For is absent, never when invalid.
	if values := r.Header.Values("X-Real-IP"); len(values) == 1 {
		if addr, ok := forwardedIP(values[0]); ok {
			return addr.String()
		}
	}
	return peer.String()
}

func forwardedIP(value string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (s *Server) isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
