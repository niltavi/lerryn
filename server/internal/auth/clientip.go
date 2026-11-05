package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// maxUserAgent bounds the user agent kept for audit records and sessions.
const maxUserAgent = 256

// TrustedProxies is server.trusted_proxies: the networks of the reverse
// proxies in front of the server. The zero value trusts no proxy.
//
// Rate limits, audit records and sessions are keyed on the client
// address. Behind a reverse proxy every request arrives from the proxy, so
// RemoteAddr alone would put all users behind one address and let ten
// failed logins from anyone lock everyone out. X-Forwarded-For carries the
// real address, but any client can send that header, so it is believed
// only when the peer is a configured proxy.
type TrustedProxies struct {
	prefixes []netip.Prefix
}

// ParseTrustedProxies parses CIDR networks such as 172.30.0.0/24.
func ParseTrustedProxies(cidrs []string) (TrustedProxies, error) {
	var t TrustedProxies
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return TrustedProxies{}, fmt.Errorf("trusted proxy %q is not a CIDR network: %w", c, err)
		}
		t.prefixes = append(t.prefixes, p.Masked())
	}
	return t, nil
}

func (t TrustedProxies) trusts(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range t.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the address of the client that sent r. When the peer
// is a trusted proxy it walks X-Forwarded-For from the right, skipping
// further trusted proxies, and returns the first address that is not one:
// that entry was written by a proxy we trust, while everything to its left
// may have been made up by the client. Otherwise it returns the peer
// address.
func (t TrustedProxies) ClientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if len(t.prefixes) == 0 {
		return peer
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil || !t.trusts(addr) {
		return peer
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	client := addr.Unmap()
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseHop(hops[i])
		if !ok {
			// A trusted proxy wrote something we cannot read; the last
			// address we could vouch for is the best answer left.
			break
		}
		client = hop
		if !t.trusts(hop) {
			break
		}
	}
	return client.String()
}

// parseHop reads one X-Forwarded-For entry. Entries are bare addresses;
// some proxies add a port, which is dropped.
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// Client returns the client of r for audit records, sessions and rate
// limits.
func (t TrustedProxies) Client(r *http.Request) Client {
	ua := r.UserAgent()
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	return Client{IP: t.ClientIP(r), UserAgent: ua}
}
