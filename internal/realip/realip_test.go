package realip

import (
	"context"
	"net/http"
	"net/netip"
	"testing"
)

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

func TestResolverClientIP(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		remote  string
		xff     []string
		want    string
	}{
		{"no trusted ignores xff", nil, "10.0.0.2:5000", []string{"203.0.113.9"}, "10.0.0.2"},
		{"untrusted remote ignores forged xff", []string{"10.0.0.0/8"}, "198.51.100.1:5000", []string{"203.0.113.9"}, "198.51.100.1"},
		{"trusted proxy single xff", []string{"10.0.0.0/8"}, "10.0.0.2:5000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"forged leftmost ignored", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"198.51.100.7, 203.0.113.9, 10.0.0.2"}, "203.0.113.9"},
		{"multiple header lines", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"198.51.100.7", "203.0.113.9, 10.0.0.2"}, "203.0.113.9"},
		{"all trusted returns leftmost", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"10.0.0.5, 10.0.0.2"}, "10.0.0.5"},
		{"garbage stops walk", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"198.51.100.7, garbage, 10.0.0.2"}, "10.0.0.2"},
		{"garbage right after remote", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"198.51.100.7, garbage"}, "10.0.0.1"},
		{"xff with port", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"203.0.113.9:4711"}, "203.0.113.9"},
		{"xff ipv6 bracket port", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"[2001:db8::1]:443"}, "2001:db8::1"},
		{"xff ipv4-mapped untrusted", []string{"10.0.0.0/8"}, "10.0.0.1:5000", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"remote ipv6", []string{"2001:db8::/32"}, "[2001:db8::1]:443", []string{"203.0.113.9"}, "203.0.113.9"},
		{"remote ipv4-mapped matches", []string{"10.0.0.0/8"}, "[::ffff:10.0.0.2]:443", []string{"203.0.113.9"}, "203.0.113.9"},
		{"no xff", []string{"10.0.0.0/8"}, "10.0.0.2:5000", nil, "10.0.0.2"},
		{"remote without port", nil, "192.0.2.1", nil, "192.0.2.1"},
		{"remote not an ip", []string{"10.0.0.0/8"}, "@", []string{"203.0.113.9"}, "@"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{RemoteAddr: tc.remote, Header: http.Header{}}
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			got := NewResolver(prefixes(t, tc.trusted...)).ClientIP(req)
			if got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolverTrusted(t *testing.T) {
	r := NewResolver(prefixes(t, "10.0.0.0/8"))
	if !r.Trusted(netip.MustParseAddr("::ffff:10.1.2.3")) {
		t.Fatal("mapped address should match")
	}
	if r.Trusted(netip.MustParseAddr("11.0.0.1")) {
		t.Fatal("outside address should not match")
	}
}

func TestFromRequest(t *testing.T) {
	req := &http.Request{RemoteAddr: "192.0.2.1:99", Header: http.Header{}}
	if got := FromRequest(req); got != "192.0.2.1" {
		t.Fatalf("fallback = %q", got)
	}
	req = req.WithContext(WithClientIP(context.Background(), "203.0.113.9"))
	if got := FromRequest(req); got != "203.0.113.9" {
		t.Fatalf("context = %q", got)
	}
}
