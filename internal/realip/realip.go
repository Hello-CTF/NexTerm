// Package realip resolves the client address of a request that may have passed through trusted reverse proxies.
package realip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type ctxKey struct{}

// Resolver decides which peers are trusted proxies. The zero value trusts none.
// trustForwardedFor is an explicit opt-in so that deployments which listed a proxy only for X-Forwarded-Proto do
// not start trusting X-Forwarded-For; a proxy that overwrites or appends to it is safe, one that passes the
// client's header through unchanged is not.
type Resolver struct {
	trusted           []netip.Prefix
	trustForwardedFor bool
}

func NewResolver(trusted []netip.Prefix, trustForwardedFor bool) Resolver {
	return Resolver{trusted: append([]netip.Prefix(nil), trusted...), trustForwardedFor: trustForwardedFor}
}

func (r Resolver) Trusted(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	for _, p := range r.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientIP returns the RemoteAddr host unless X-Forwarded-For is trusted and the peer is a trusted proxy, in which case X-Forwarded-For is
// walked from the right and the first non-trusted address wins. Parsing stops at the first malformed entry.
// X-Real-IP and Forwarded are deliberately ignored.
func (r Resolver) ClientIP(req *http.Request) string {
	host := remoteHost(req)
	hop, err := netip.ParseAddr(host)
	if !r.trustForwardedFor || err != nil || !r.Trusted(hop) {
		return host
	}
	var items []string
	for _, line := range req.Header.Values("X-Forwarded-For") {
		for _, item := range strings.Split(line, ",") {
			items = append(items, strings.TrimSpace(item))
		}
	}
	nearest := hop.Unmap().WithZone("")
	for i := len(items) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(items[i])
		if err != nil {
			ap, apErr := netip.ParseAddrPort(items[i])
			if apErr != nil {
				return nearest.String()
			}
			addr = ap.Addr()
		}
		addr = addr.Unmap().WithZone("")
		if !r.Trusted(addr) {
			return addr.String()
		}
		nearest = addr
	}
	return nearest.String()
}

// RemoteHost returns the direct peer address of req without consulting forwarding headers.
func RemoteHost(req *http.Request) string { return remoteHost(req) }

func remoteHost(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ctxKey{}, ip)
}

// FromRequest returns the address resolved by the transport guard, or the direct peer when none was resolved.
func FromRequest(r *http.Request) string {
	if ip, ok := r.Context().Value(ctxKey{}).(string); ok {
		return ip
	}
	return remoteHost(r)
}
