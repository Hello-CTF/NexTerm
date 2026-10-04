package ssh

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPConnectPreservesBufferedBytesAndAuthentication(t *testing.T) {
	listener := newTCPListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			serverErr <- err
			return
		}
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if req.Method != http.MethodConnect || req.Host != "example.test:22" || req.Header.Get("Proxy-Authorization") != expected {
			serverErr <- fmt.Errorf("unexpected CONNECT request: %s %s %q", req.Method, req.Host, req.Header.Get("Proxy-Authorization"))
			return
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\nSSH-2.0-early\r\n")
		serverErr <- err
	}()
	proxyURL := "http://user:pass@" + listener.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialSSH(ctx, Config{ProxyURL: proxyURL}, "example.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	banner := make([]byte, len("SSH-2.0-early\r\n"))
	if _, err := io.ReadFull(conn, banner); err != nil || string(banner) != "SSH-2.0-early\r\n" {
		t.Fatalf("buffered banner = %q, %v", banner, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPConnectRejection(t *testing.T) {
	listener := newTCPListener(t)
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_, _ = http.ReadRequest(bufio.NewReader(conn))
			_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dialSSH(ctx, Config{ProxyURL: "http://" + listener.Addr().String()}, "example.test", 22); err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("rejected CONNECT = %v", err)
	}
}

func TestSOCKS5AuthenticationAndRemoteDNS(t *testing.T) {
	listener := newTCPListener(t)
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		if err := serveSOCKS5(conn); err != nil {
			serverErr <- err
			return
		}
		_, err = conn.Write([]byte("proxied"))
		serverErr <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialSSH(ctx, Config{ProxyURL: "socks5://user:pass@" + listener.Addr().String()}, "example.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	response := make([]byte, 7)
	if _, err := io.ReadFull(conn, response); err != nil || string(response) != "proxied" {
		t.Fatalf("SOCKS response = %q, %v", response, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestDirectDialIgnoresSystemProxyEnvironment(t *testing.T) {
	listener := newTCPListener(t)
	defer listener.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	host, port := splitAddress(t, listener.Addr().String())
	conn, err := dialSSH(context.Background(), Config{}, host, port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestUnsupportedProxyScheme(t *testing.T) {
	if _, err := dialSSH(context.Background(), Config{ProxyURL: "https://proxy.example:8443"}, "example.test", 22); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("HTTPS proxy = %v", err)
	}
}

func serveSOCKS5(conn net.Conn) error {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != 5 {
		return fmt.Errorf("SOCKS version = %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != 1 {
		return fmt.Errorf("SOCKS auth version = %d", header[0])
	}
	user := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}
	var passwordLength [1]byte
	if _, err := io.ReadFull(conn, passwordLength[:]); err != nil {
		return err
	}
	password := make([]byte, int(passwordLength[0]))
	if _, err := io.ReadFull(conn, password); err != nil {
		return err
	}
	if string(user) != "user" || string(password) != "pass" {
		return fmt.Errorf("SOCKS credentials = %q/%q", user, password)
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return err
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return err
	}
	if request[0] != 5 || request[1] != 1 || request[3] != 3 {
		return fmt.Errorf("SOCKS request = %v (want remote DNS CONNECT)", request)
	}
	var length [1]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return err
	}
	host := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, host); err != nil {
		return err
	}
	var port uint16
	if err := binary.Read(conn, binary.BigEndian, &port); err != nil {
		return err
	}
	if string(host) != "example.test" || port != 22 {
		return fmt.Errorf("SOCKS target = %s:%d", host, port)
	}
	_, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	return err
}

func newTCPListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func splitAddress(t *testing.T, address string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return host, port
}
