package httpapi

import (
	"net"
	"net/http"
	"strings"
)

// clientIPResolver derives the client address used for rate limiting and
// request logging. Forwarding headers are only honoured when the transport
// peer itself is a trusted proxy; a direct caller cannot spoof them.
//
// The deployment chain is Cloudflare -> host nginx (loopback) -> API, so the
// peer is 127.0.0.1 in production and nginx appends the restored real client
// IP to X-Forwarded-For.
type clientIPResolver struct {
	trusted []*net.IPNet
}

// newClientIPResolver parses CIDR strings. Invalid entries are ignored; config
// validation rejects them before the server starts.
func newClientIPResolver(cidrs []string) clientIPResolver {
	resolver := clientIPResolver{}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err == nil {
			resolver.trusted = append(resolver.trusted, network)
		}
	}
	return resolver
}

// defaultClientIPResolver trusts loopback only, matching the shipped config.
var defaultClientIPResolver = newClientIPResolver([]string{"127.0.0.0/8", "::1/128"})

func (r clientIPResolver) trusts(ip net.IP) bool {
	for _, network := range r.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// resolve returns the client IP for keying rate limits and logging.
//
// For a trusted peer it prefers the rightmost non-trusted X-Forwarded-For hop:
// nginx appends the address it actually saw, so a client-supplied prefix
// cannot override it. It falls back to X-Real-IP and finally to the peer.
// For an untrusted peer, headers are ignored entirely.
func (r clientIPResolver) resolve(request *http.Request) string {
	peer := request.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil && host != "" {
		peer = host
	}
	if peer == "" {
		return "unknown"
	}

	peerIP := net.ParseIP(peer)
	if peerIP == nil || !r.trusts(peerIP) {
		return peer
	}

	hops := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := net.ParseIP(strings.TrimSpace(hops[i]))
		if hop == nil {
			continue
		}
		if !r.trusts(hop) {
			return hop.String()
		}
	}

	if realIP := net.ParseIP(strings.TrimSpace(request.Header.Get("X-Real-IP"))); realIP != nil {
		return realIP.String()
	}

	return peer
}
