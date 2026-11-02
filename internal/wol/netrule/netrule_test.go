package netrule

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestDeniedAndPrivate(t *testing.T) {
	for _, denied := range []string{"0.0.0.0", "0.1.2.3", "127.0.0.1", "127.255.255.255", "169.254.169.254", "224.0.0.1", "239.255.255.255",
		"100.64.0.1", "100.127.255.255", "240.0.0.1", "255.255.255.254", "::1"} {
		if Denied(netip.MustParseAddr(denied)) == "" {
			t.Errorf("%s allowed", denied)
		}
	}
	for _, allowed := range []string{"255.255.255.255", "192.168.1.255", "10.1.2.3", "172.16.0.1", "8.8.8.8", "100.63.255.255", "100.128.0.1", "172.32.0.1"} {
		if reason := Denied(netip.MustParseAddr(allowed)); reason != "" {
			t.Errorf("%s denied: %s", allowed, reason)
		}
	}
	for addr, want := range map[string]bool{"255.255.255.255": true, "10.0.0.1": true, "172.16.5.5": true, "172.31.255.255": true, "192.168.0.1": true, "8.8.8.8": false, "172.32.0.1": false, "100.64.0.1": false} {
		if got := Private(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Private(%s) = %v", addr, got)
		}
	}
}

func TestIsMagicPacket(t *testing.T) {
	packet := bytes.Repeat([]byte{0xFF}, 6)
	for i := 0; i < 16; i++ {
		packet = append(packet, 1, 2, 3, 4, 5, 6)
	}
	if !IsMagicPacket(packet) {
		t.Fatal("valid packet rejected")
	}
	broken := append([]byte(nil), packet...)
	broken[101] = 7
	if IsMagicPacket(broken) || IsMagicPacket(packet[:101]) || IsMagicPacket(append(packet, 0)) || IsMagicPacket(nil) {
		t.Fatal("invalid packet accepted")
	}
}
