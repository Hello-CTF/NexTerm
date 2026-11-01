// Package netrule holds the address and payload rules of Wake-on-LAN that both the server-side service and the
// device agent apply. It depends on the standard library only, so the static agent stays small.
package netrule

import (
	"bytes"
	"net/netip"
)

// PacketSize is 6 bytes of 0xFF followed by the MAC sixteen times.
const PacketSize = 6 + 16*6

var (
	limitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})
	thisNetwork      = netip.MustParsePrefix("0.0.0.0/8")
	loopback         = netip.MustParsePrefix("127.0.0.0/8")
	linkLocal        = netip.MustParsePrefix("169.254.0.0/16")
	multicast        = netip.MustParsePrefix("224.0.0.0/4")
	sharedAddress    = netip.MustParsePrefix("100.64.0.0/10")
	reserved         = netip.MustParsePrefix("240.0.0.0/4")
)

// Denied returns the reason an address can never be a wake target, or "" when it is not on the always-deny list:
// 0/8, 127/8, 169.254/16, multicast, 100.64/10 and 240/4 (the limited broadcast address 255.255.255.255 excepted).
// Only IPv4 literals are accepted at all.
func Denied(addr netip.Addr) string {
	if !addr.Is4() {
		return "只支持 IPv4 地址"
	}
	if addr == limitedBroadcast {
		return ""
	}
	switch {
	case thisNetwork.Contains(addr):
		return "不能是 0.0.0.0/8 地址"
	case loopback.Contains(addr):
		return "不能是回环地址"
	case linkLocal.Contains(addr):
		return "不能是链路本地地址"
	case multicast.Contains(addr):
		return "不能是组播地址"
	case sharedAddress.Contains(addr):
		return "不能是 100.64.0.0/10 共享地址"
	case reserved.Contains(addr):
		return "不能是 240.0.0.0/4 保留地址"
	}
	return ""
}

// Private reports whether a non-denied address is the limited broadcast or in RFC 1918 space; server-mode users
// without the superadmin role may only target these.
func Private(addr netip.Addr) bool {
	return addr == limitedBroadcast || (addr.Is4() && addr.IsPrivate())
}

// IsMagicPacket reports whether payload is exactly 102 bytes: six 0xFF bytes then sixteen identical 6-byte MACs.
func IsMagicPacket(payload []byte) bool {
	if len(payload) != PacketSize || !bytes.Equal(payload[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		return false
	}
	mac := payload[6:12]
	for i := 1; i < 16; i++ {
		if !bytes.Equal(payload[6+i*6:12+i*6], mac) {
			return false
		}
	}
	return true
}
