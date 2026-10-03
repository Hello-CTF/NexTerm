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

func dialSSH(ctx context.Context, proxyURL, host string, port int) (net.Conn, error) {
	target := endpointString(host, port)
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	if proxyURL == "" {
		return dialer.DialContext(ctx, "tcp", target)
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse explicit SSH proxy: %w", err)
	}
	if u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("invalid explicit SSH proxy URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: password}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, dialer)
		if err != nil {
			return nil, fmt.Errorf("configure SOCKS5 proxy: %w", err)
		}
		contextDialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("configure SOCKS5 proxy: context-aware dialing unsupported")
		}
		conn, err := contextDialer.DialContext(ctx, "tcp", target)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 proxy connect: %w", err)
		}
		return conn, nil
	case "http":
		conn, err := dialer.DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return nil, fmt.Errorf("HTTP proxy connect: %w", err)
		}
		tunnel, err := httpConnect(ctx, conn, u, target)
		if err != nil {
			conn.Close()
			return nil, err
		}
		return tunnel, nil
	default:
		return nil, fmt.Errorf("unsupported SSH proxy scheme %q (use socks5:// or http://)", u.Scheme)
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
