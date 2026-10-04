package ssh

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const maxProxyResponseBytes = 64 << 10

func dialSSH(ctx context.Context, cfg Config, host string, port int) (net.Conn, error) {
	target := endpointString(host, port)
	if cfg.ProxyCommand != "" {
		conn, err := dialProxyCommand(ctx, cfg.ProxyCommand, host, port)
		if err != nil {
			return nil, &proxyError{endpoint: "proxy-command", err: err}
		}
		return conn, nil
	}
	proxyURL := cfg.ProxyURL
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	if proxyURL == "" {
		return dialTCP(ctx, dialer, target)
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, &proxyError{err: fmt.Errorf("parse SSH proxy URL: invalid URL")}
	}
	if u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, &proxyError{endpoint: proxyEndpoint(u), err: fmt.Errorf("invalid SSH proxy URL")}
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: password}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, noDelayDialer{dialer})
		if err != nil {
			return nil, &proxyError{endpoint: proxyEndpoint(u), err: fmt.Errorf("configure SOCKS5 proxy: %w", err)}
		}
		contextDialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, &proxyError{endpoint: proxyEndpoint(u), err: fmt.Errorf("configure SOCKS5 proxy: context-aware dialing unsupported")}
		}
		conn, err := contextDialer.DialContext(ctx, "tcp", target)
		if err != nil {
			return nil, &proxyError{endpoint: proxyEndpoint(u), err: fmt.Errorf("SOCKS5 connect: %w", err)}
		}
		return conn, nil
	case "http":
		conn, err := dialTCP(ctx, dialer, u.Host)
		if err != nil {
			return nil, &proxyError{endpoint: proxyEndpoint(u), err: fmt.Errorf("connect: %w", err)}
		}
		tunnel, err := httpConnect(ctx, conn, u, target)
		if err != nil {
			conn.Close()
			return nil, &proxyError{endpoint: proxyEndpoint(u), err: err}
		}
		return tunnel, nil
	default:
		return nil, &proxyError{endpoint: proxyEndpoint(u), err: fmt.Errorf("unsupported SSH proxy scheme %q (use socks5:// or http://)", u.Scheme)}
	}
}

func dialTCP(ctx context.Context, dialer *net.Dialer, target string) (net.Conn, error) {
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	setNoDelay(conn)
	return conn, nil
}

type noDelayDialer struct{ base *net.Dialer }

func (d noDelayDialer) Dial(network, address string) (net.Conn, error) {
	conn, err := d.base.Dial(network, address)
	if err != nil {
		return nil, err
	}
	setNoDelay(conn)
	return conn, nil
}

func (d noDelayDialer) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	return dialTCP(ctx, d.base, address)
}

func setNoDelay(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
}

func httpConnect(ctx context.Context, conn net.Conn, proxyURL *url.URL, target string) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	header := make(http.Header)
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		header.Set("Proxy-Authorization", "Basic "+token)
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: header,
	}
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("write HTTP CONNECT request: %w", err)
	}
	capped := &cappedReader{reader: conn, remaining: maxProxyResponseBytes}
	reader := bufio.NewReader(capped)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP CONNECT rejected: %s", resp.Status)
	}
	capped.remaining = -1
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type cappedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *cappedReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return r.reader.Read(p)
	}
	if r.remaining == 0 {
		return 0, fmt.Errorf("HTTP proxy response exceeds %d bytes", maxProxyResponseBytes)
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
