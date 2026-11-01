// Package wol sends Wake-on-LAN magic packets for stored assets. The destination is only ever derived from the
// stored asset options; nothing a page supplies reaches the socket.
package wol

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/wol/netrule"
)

const (
	DefaultBroadcast = "255.255.255.255"
	DefaultPort      = 9
	// PacketSize is 6 bytes of 0xFF followed by the MAC sixteen times.
	PacketSize = netrule.PacketSize

	// DevicePrefix names fleet devices in an asset's viaAssetId.
	DevicePrefix = "device/"
)

// ParseMAC accepts exactly six hex octets of two digits separated by one consistent ':' or '-'. Bare, dotted and
// shortened forms are rejected, as are the all-zero and broadcast addresses, which can never identify a host.
func ParseMAC(raw string) (net.HardwareAddr, error) {
	text := strings.TrimSpace(raw)
	if len(text) != 17 {
		return nil, badMAC()
	}
	sep := text[2]
	if sep != ':' && sep != '-' {
		return nil, badMAC()
	}
	mac := make(net.HardwareAddr, 6)
	for i := 0; i < 6; i++ {
		part := text[i*3 : i*3+2]
		if i < 5 && text[i*3+2] != sep {
			return nil, badMAC()
		}
		for _, c := range []byte(part) {
			if !isHex(c) {
				return nil, badMAC()
			}
		}
		value, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return nil, badMAC()
		}
		mac[i] = byte(value)
	}
	allZero, allFF := true, true
	for _, b := range mac {
		allZero = allZero && b == 0
		allFF = allFF && b == 0xFF
	}
	if allZero || allFF {
		return nil, badMAC()
	}
	return mac, nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func badMAC() error {
	return ipc.NewError(ipc.CodeBadParam, "MAC 地址格式无效，请使用 AA:BB:CC:DD:EE:FF 或 AA-BB-CC-DD-EE-FF")
}

// MagicPacket builds the 102-byte payload: six 0xFF bytes then the MAC sixteen times.
func MagicPacket(mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, badMAC()
	}
	packet := make([]byte, 0, PacketSize)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}
	return packet, nil
}

// Options are the wake keys read from an asset's OptionsJSON.
type Options struct {
	ViaAssetID    string `json:"viaAssetId"`
	WakeMac       string `json:"wakeMac"`
	WakeBroadcast string `json:"wakeBroadcast"`
	WakePort      *int   `json:"wakePort"`
}

// ParseOptions reads and validates the wake keys. An asset without wakeMac is valid (nothing to wake); broadcast
// and port are still checked when present so a bad value cannot be stored.
func ParseOptions(raw string) (Options, error) {
	var options Options
	if strings.TrimSpace(raw) == "" {
		return options, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return options, nil
	}
	for _, key := range []string{"wakeMac", "wakeBroadcast"} {
		if encoded, ok := fields[key]; ok && string(encoded) != "null" {
			var text string
			if err := json.Unmarshal(encoded, &text); err != nil {
				return Options{}, ipc.NewError(ipc.CodeBadParam, key+" 必须是字符串")
			}
		}
	}
	if encoded, ok := fields["wakePort"]; ok && string(encoded) != "null" {
		var port int
		if err := json.Unmarshal(encoded, &port); err != nil {
			return Options{}, ipc.NewError(ipc.CodeBadParam, "唤醒端口必须是整数")
		}
		options.WakePort = &port
	}
	// A viaAssetId of another type is not this package's concern; it is read leniently and ignored.
	_ = json.Unmarshal(fields["viaAssetId"], &options.ViaAssetID)
	_ = json.Unmarshal(fields["wakeMac"], &options.WakeMac)
	_ = json.Unmarshal(fields["wakeBroadcast"], &options.WakeBroadcast)
	options.ViaAssetID = strings.TrimSpace(options.ViaAssetID)
	options.WakeMac = strings.TrimSpace(options.WakeMac)
	options.WakeBroadcast = strings.TrimSpace(options.WakeBroadcast)
	if options.WakeMac != "" {
		if _, err := ParseMAC(options.WakeMac); err != nil {
			return Options{}, err
		}
	}
	if _, err := options.Destination(); err != nil {
		return Options{}, err
	}
	return options, nil
}

// ValidateOptions is the write-time check used by the asset store.
func ValidateOptions(raw string) error {
	_, err := ParseOptions(raw)
	return err
}

// Destination resolves the broadcast address and port with defaults applied. Only IPv4 literals are accepted
// (no DNS names, no IPv6: IPv6 has no broadcast) and the always-deny list of netrule.Denied applies. Which of the
// remaining addresses a caller may use is decided when sending, by role.
func (o Options) Destination() (netip.AddrPort, error) {
	host := o.WakeBroadcast
	if host == "" {
		host = DefaultBroadcast
	}
	port := DefaultPort
	if o.WakePort != nil {
		port = *o.WakePort
	}
	if port < 1 || port > 65535 {
		return netip.AddrPort{}, ipc.NewError(ipc.CodeBadParam, "唤醒端口必须在 1 到 65535 之间")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.Is4() {
		return netip.AddrPort{}, ipc.NewError(ipc.CodeBadParam, "广播地址必须是 IPv4 地址（例如 192.168.1.255）")
	}
	if reason := netrule.Denied(addr); reason != "" {
		return netip.AddrPort{}, ipc.NewError(ipc.CodeBadParam, "广播地址"+reason)
	}
	return netip.AddrPortFrom(addr, uint16(port)), nil
}

// FormatMAC returns the canonical upper-case colon form.
func FormatMAC(mac net.HardwareAddr) string {
	return strings.ToUpper(mac.String())
}

// SendUDP writes one datagram to target from the local machine. SO_BROADCAST is enabled by the Go runtime for
// UDP sockets.
func SendUDP(ctx context.Context, target string, packet []byte) error {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "udp4", target)
	if err != nil {
		return fmt.Errorf("打开 UDP 套接字失败: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	n, err := conn.Write(packet)
	if err != nil {
		return fmt.Errorf("发送唤醒包失败: %w", err)
	}
	if n != len(packet) {
		return fmt.Errorf("唤醒包只发送了 %d/%d 字节", n, len(packet))
	}
	return nil
}
