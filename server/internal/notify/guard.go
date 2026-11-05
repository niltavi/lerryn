package notify

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// ErrBlockedAddress means a notification would reach an internal address
// (loopback, private, link-local and similar) that the deployment has not
// allowed.
var ErrBlockedAddress = errors.New("notify: target address is internal and not allowed")

// ErrInvalidURL means a target URL is not an absolute http or https URL.
var ErrInvalidURL = errors.New("notify: target must be an absolute http or https URL")

// internalPrefixes are special-purpose ranges that netip does not
// classify but that must not be reachable from notifications: shared
// address space, benchmarking and documentation ranges, the reserved
// block, and IPv6 transition prefixes that can embed an IPv4 address.
var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	// IPv4-compatible (deprecated) and IPv4-translated (RFC 2765)
	// addresses; IPv4-mapped ones are unmapped and checked as IPv4.
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	// Teredo embeds the server's and the client's IPv4 addresses.
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// Guard decides which addresses notifications may reach. By default only
// public unicast addresses are allowed; a deployment that sends to an
// internal receiver lists its networks in notify.allowed_networks.
//
// The check runs when the connection is made, on the address the name
// resolved to, so a hostname that resolves to an internal address, or
// starts to after the channel was saved, is caught as well.
type Guard struct {
	allowed []netip.Prefix
}

// NewGuard returns a guard that also allows the given CIDR networks.
func NewGuard(allowedNetworks []string) (*Guard, error) {
	g := &Guard{}
	for _, s := range allowedNetworks {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("notify: allowed network %q: %w", s, err)
		}
		g.allowed = append(g.allowed, p.Masked())
	}
	return g, nil
}

// Allowed reports whether a connection to ip is permitted.
func (g *Guard) Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range g.allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return !Internal(ip)
}

// Internal reports whether ip is not a public unicast address.
func Internal(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		// IsGlobalUnicast is false for loopback, link-local, multicast and
		// unspecified addresses.
		return true
	}
	for _, p := range internalPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckURL validates a target URL when a channel is saved: an absolute
// http or https URL whose host, if it is an IP address or localhost, is
// allowed. Hostnames are checked again on every connection.
func (g *Guard) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return ErrInvalidURL
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		host = "127.0.0.1"
	}
	if ip, err := netip.ParseAddr(host); err == nil && !g.Allowed(ip) {
		return ErrBlockedAddress
	}
	return nil
}

// control is a net.Dialer Control function that refuses connections to
// addresses the guard does not allow.
func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("notify: dial address %q: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("notify: dial address %q: %w", address, err)
	}
	if !g.Allowed(ip) {
		return ErrBlockedAddress
	}
	return nil
}
