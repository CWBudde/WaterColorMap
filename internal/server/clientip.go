package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ProxyPolicy decides whether forwarding headers on a request can be believed.
//
// X-Forwarded-For is attacker-controlled unless the request demonstrably came
// through a proxy you operate. Trusting it unconditionally makes every per-IP
// limit bypassable with a single header, so the zero value trusts nothing.
type ProxyPolicy struct {
	// Trusted lists proxy networks. Empty means forwarding headers are ignored.
	Trusted []netip.Prefix
}

// ParseProxyPolicy builds a policy from a comma-separated list of CIDRs. Bare
// addresses are accepted and treated as single-host prefixes.
func ParseProxyPolicy(cidrs string) (ProxyPolicy, error) {
	var policy ProxyPolicy

	for _, raw := range strings.Split(cidrs, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}

		if prefix, err := netip.ParsePrefix(entry); err == nil {
			policy.Trusted = append(policy.Trusted, prefix)
			continue
		}

		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return ProxyPolicy{}, err
		}
		policy.Trusted = append(policy.Trusted, netip.PrefixFrom(addr, addr.BitLen()))
	}

	return policy, nil
}

func (p ProxyPolicy) trusts(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range p.Trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientKey returns the rate-limit bucket key for r.
//
// It is the peer address, unless the peer is a trusted proxy, in which case it
// is the rightmost X-Forwarded-For hop that is not itself trusted — the
// standard non-spoofable rule, since an attacker can only prepend entries.
//
// IPv6 addresses are truncated to their /64. A single IPv6 client normally
// controls its whole /64, so without this it could rotate through effectively
// unlimited addresses and defeat both the limit and the entry cap.
func (p ProxyPolicy) ClientKey(r *http.Request) netip.Addr {
	peer := parseAddr(r.RemoteAddr)
	if !p.trusts(peer) {
		return normalize(peer)
	}

	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		hops := strings.Split(forwarded, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				continue
			}
			if !p.trusts(addr) {
				return normalize(addr)
			}
		}
	}

	if realIP := r.Header.Get("X-Real-Ip"); realIP != "" {
		if addr, err := netip.ParseAddr(strings.TrimSpace(realIP)); err == nil {
			return normalize(addr)
		}
	}

	// Every hop was trusted (or unparseable): fall back to the peer.
	return normalize(peer)
}

// parseAddr extracts the address from a "host:port" RemoteAddr. An
// unparseable value yields the zero Addr, which still works as a map key —
// all such requests simply share one bucket.
func parseAddr(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// normalize collapses an IPv6 address to its /64 network so a client cannot
// escape its bucket by rotating within its own prefix.
func normalize(addr netip.Addr) netip.Addr {
	if !addr.IsValid() || !addr.Is6() {
		return addr
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr
	}
	return prefix.Addr()
}
