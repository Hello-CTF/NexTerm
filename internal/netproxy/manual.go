package netproxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Manual proxy types. An empty Type means no manual proxy.
const (
	TypeHTTP   = "http"
	TypeSOCKS4 = "socks4"
	TypeSOCKS5 = "socks5"
)

// Manual is a proxy the user configured for one connection. SOCKS4 carries
// only a user ID, so it takes a username and never a password.
type Manual struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Enabled reports whether a manual proxy is configured.
func (m Manual) Enabled() bool { return m.Type != "" }

// Normalize trims the fields, clears them all when no type is set, and
// rejects an incomplete or unsupported proxy.
func (m Manual) Normalize() (Manual, error) {
	m.Type = strings.ToLower(strings.TrimSpace(m.Type))
	if m.Type == "" {
		return Manual{}, nil
	}
	m.Host = strings.TrimSpace(m.Host)
	m.Username = strings.TrimSpace(m.Username)
	switch m.Type {
	case TypeHTTP, TypeSOCKS4, TypeSOCKS5:
	default:
		return Manual{}, fmt.Errorf("不支持的代理类型 %q，可选 http、socks4、socks5", m.Type)
	}
	if m.Host == "" || strings.ContainsAny(m.Host, "/@ ") {
		return Manual{}, errors.New("代理地址只填写主机名或 IP，不含协议、路径和账号")
	}
	if strings.HasPrefix(m.Host, "[") && strings.HasSuffix(m.Host, "]") {
		m.Host = m.Host[1 : len(m.Host)-1]
	}
	if m.Port < 1 || m.Port > 65535 {
		return Manual{}, errors.New("代理端口须在 1-65535 之间")
	}
	if m.Type == TypeSOCKS4 && m.Password != "" {
		return Manual{}, errors.New("SOCKS4 代理不支持密码，只能填写用户名")
	}
	if m.Username == "" && m.Password != "" {
		return Manual{}, errors.New("填写代理密码时也需要填写代理用户名")
	}
	return m, nil
}

func (m Manual) address() string { return net.JoinHostPort(m.Host, strconv.Itoa(m.Port)) }

// Apply routes transport through the manual proxy. Without one the transport
// is left as it is. HTTP and SOCKS5 use the transport's own proxy support;
// SOCKS4 dials through the proxy, so TLS still runs end to end.
func (m Manual) Apply(transport *http.Transport, dialer *net.Dialer) error {
	m, err := m.Normalize()
	if err != nil || !m.Enabled() {
		return err
	}
	switch m.Type {
	case TypeHTTP, TypeSOCKS5:
		proxyURL := &url.URL{Scheme: m.Type, Host: m.address()}
		if m.Username != "" {
			proxyURL.User = url.UserPassword(m.Username, m.Password)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	case TypeSOCKS4:
		if dialer == nil {
			dialer = &net.Dialer{}
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialSOCKS4(ctx, dialer, m, network, address)
		}
	}
	return nil
}

const (
	socks4Version = 4
	socks4Connect = 1
	socks4Granted = 90
)

// dialSOCKS4 connects to address through a SOCKS4 proxy. An IPv4 target is
// sent as is; a host name uses SOCKS4a so the proxy resolves it and no DNS
// query leaves this machine.
func dialSOCKS4(ctx context.Context, dialer *net.Dialer, proxy Manual, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("socks4 proxy: unsupported network %s", network)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("socks4 proxy: invalid port %q", portText)
	}
	request := []byte{socks4Version, socks4Connect}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	hostname := ""
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !ip.Is4() {
			return nil, errors.New("socks4 proxy: IPv6 targets are not supported")
		}
		addr := ip.As4()
		request = append(request, addr[:]...)
	} else {
		request = append(request, 0, 0, 0, 1)
		hostname = host
	}
	request = append(request, proxy.Username...)
	request = append(request, 0)
	if hostname != "" {
		request = append(request, hostname...)
		request = append(request, 0)
	}

	conn, err := dialer.DialContext(ctx, "tcp", proxy.address())
	if err != nil {
		return nil, fmt.Errorf("socks4 proxy: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	reply := make([]byte, 8)
	_, err = conn.Write(request)
	if err == nil {
		_, err = io.ReadFull(conn, reply)
	}
	if !stop() {
		// The context ended the handshake by expiring the deadline: report why.
		err = ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socks4 proxy: %w", err)
	}
	if reply[1] != socks4Granted {
		_ = conn.Close()
		return nil, fmt.Errorf("socks4 proxy rejected the connection (code %d)", reply[1])
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
