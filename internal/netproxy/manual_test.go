package netproxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualNormalize(t *testing.T) {
	got, err := Manual{Type: " SOCKS5 ", Host: " [::1] ", Port: 1080, Username: " u ", Password: "p"}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if want := (Manual{Type: TypeSOCKS5, Host: "::1", Port: 1080, Username: "u", Password: "p"}); got != want {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}
	if got, err := (Manual{Host: "leftover", Port: 1, Password: "x"}).Normalize(); err != nil || got.Enabled() || got != (Manual{}) {
		t.Fatalf("no type = %+v %v, want an empty proxy", got, err)
	}
	for _, invalid := range []Manual{
		{Type: "https", Host: "proxy", Port: 1},
		{Type: TypeHTTP, Port: 8080},
		{Type: TypeHTTP, Host: "http://proxy", Port: 8080},
		{Type: TypeHTTP, Host: "proxy", Port: 0},
		{Type: TypeHTTP, Host: "proxy", Port: 65536},
		{Type: TypeSOCKS4, Host: "proxy", Port: 1080, Username: "u", Password: "p"},
		{Type: TypeSOCKS5, Host: "proxy", Port: 1080, Password: "p"},
	} {
		if _, err := invalid.Normalize(); err == nil {
			t.Errorf("Normalize(%+v) accepted an invalid proxy", invalid)
		}
		if err := invalid.Apply(&http.Transport{}, nil); err == nil {
			t.Errorf("Apply(%+v) accepted an invalid proxy", invalid)
		}
	}
}

func TestManualApplyWithoutProxyKeepsTransport(t *testing.T) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if err := (Manual{}).Apply(transport, nil); err != nil {
		t.Fatal(err)
	}
	if transport.Proxy == nil || transport.DialContext != nil {
		t.Fatal("an empty manual proxy must leave the transport unchanged")
	}
}

// proxiedGet fetches the target through the manual proxy and returns the body.
func proxiedGet(t *testing.T, proxy Manual, target string) string {
	t.Helper()
	transport := &http.Transport{}
	if err := proxy.Apply(transport, nil); err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func newTarget(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	t.Cleanup(target.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	return target, port
}

// serveProxy accepts connections on a loopback listener and hands each to handle.
func serveProxy(t *testing.T, handle func(net.Conn)) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				handle(conn)
			}()
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return address.IP.String(), address.Port
}

func relay(client net.Conn, target string) {
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()
	go func() { _, _ = io.Copy(upstream, client) }()
	_, _ = io.Copy(client, upstream)
}

func TestManualHTTPProxyAuthenticates(t *testing.T) {
	target, _ := newTarget(t)
	var authorized atomic.Bool
	host, port := serveProxy(t, func(conn net.Conn) {
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		// "Basic dTpw" is u:p.
		authorized.Store(request.Header.Get("Proxy-Authorization") == "Basic dTpw")
		response, err := http.DefaultTransport.RoundTrip(request)
		if err != nil {
			return
		}
		defer response.Body.Close()
		_ = response.Write(conn)
	})
	got := proxiedGet(t, Manual{Type: TypeHTTP, Host: host, Port: port, Username: "u", Password: "p"}, target.URL)
	if got != "target" || !authorized.Load() {
		t.Fatalf("body %q authorized %v", got, authorized.Load())
	}
}

func TestManualSOCKS5ProxyAuthenticates(t *testing.T) {
	target, _ := newTarget(t)
	var credentials atomic.Value
	host, port := serveProxy(t, func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(reader, greeting); err != nil {
			return
		}
		if _, err := io.ReadFull(reader, make([]byte, greeting[1])); err != nil {
			return
		}
		_, _ = conn.Write([]byte{5, 2})
		auth := make([]byte, 2)
		if _, err := io.ReadFull(reader, auth); err != nil {
			return
		}
		user := make([]byte, auth[1])
		_, _ = io.ReadFull(reader, user)
		passLen, _ := reader.ReadByte()
		pass := make([]byte, passLen)
		_, _ = io.ReadFull(reader, pass)
		credentials.Store(string(user) + ":" + string(pass))
		_, _ = conn.Write([]byte{1, 0})
		header := make([]byte, 4)
		if _, err := io.ReadFull(reader, header); err != nil {
			return
		}
		var hostName string
		switch header[3] {
		case 1:
			ip := make([]byte, 4)
			_, _ = io.ReadFull(reader, ip)
			hostName = net.IP(ip).String()
		case 3:
			size, _ := reader.ReadByte()
			name := make([]byte, size)
			_, _ = io.ReadFull(reader, name)
			hostName = string(name)
		default:
			return
		}
		portBytes := make([]byte, 2)
		_, _ = io.ReadFull(reader, portBytes)
		_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		relay(&bufferedConn{Conn: conn, reader: reader}, net.JoinHostPort(hostName, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))
	})
	got := proxiedGet(t, Manual{Type: TypeSOCKS5, Host: host, Port: port, Username: "u", Password: "p"}, target.URL)
	if got != "target" || credentials.Load() != "u:p" {
		t.Fatalf("body %q credentials %v", got, credentials.Load())
	}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type socks4Request struct {
	port   int
	ip     net.IP
	userID string
	host   string
}

// fakeSOCKS4 serves SOCKS4 and SOCKS4a, relaying names it is given to loopback.
func fakeSOCKS4(t *testing.T, granted byte, seen chan<- socks4Request) (string, int) {
	t.Helper()
	return serveProxy(t, func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader, header); err != nil || header[0] != 4 || header[1] != 1 {
			return
		}
		request := socks4Request{port: int(binary.BigEndian.Uint16(header[2:4])), ip: net.IP(header[4:8])}
		userID, err := reader.ReadString(0)
		if err != nil {
			return
		}
		request.userID = strings.TrimSuffix(userID, "\x00")
		target := request.ip.String()
		if header[4] == 0 && header[5] == 0 && header[6] == 0 && header[7] != 0 {
			host, err := reader.ReadString(0)
			if err != nil {
				return
			}
			request.host = strings.TrimSuffix(host, "\x00")
			target = "127.0.0.1"
		}
		seen <- request
		_, _ = conn.Write([]byte{0, granted, 0, 0, 0, 0, 0, 0})
		if granted != socks4Granted {
			return
		}
		relay(&bufferedConn{Conn: conn, reader: reader}, net.JoinHostPort(target, strconv.Itoa(request.port)))
	})
}

func TestManualSOCKS4ProxySendsAddressAndUserID(t *testing.T) {
	_, targetPort := newTarget(t)
	seen := make(chan socks4Request, 2)
	host, port := fakeSOCKS4(t, socks4Granted, seen)
	proxy := Manual{Type: TypeSOCKS4, Host: host, Port: port, Username: "ops"}

	if got := proxiedGet(t, proxy, "http://127.0.0.1:"+targetPort+"/"); got != "target" {
		t.Fatalf("SOCKS4 body = %q", got)
	}
	request := <-seen
	if !request.ip.Equal(net.IPv4(127, 0, 0, 1)) || request.host != "" || request.userID != "ops" || strconv.Itoa(request.port) != targetPort {
		t.Fatalf("SOCKS4 request = %+v", request)
	}

	if got := proxiedGet(t, proxy, "http://target.invalid:"+targetPort+"/"); got != "target" {
		t.Fatalf("SOCKS4a body = %q", got)
	}
	if request := <-seen; request.host != "target.invalid" || request.userID != "ops" {
		t.Fatalf("SOCKS4a request = %+v, want the name resolved by the proxy", request)
	}
}

func TestManualSOCKS4ProxyRejection(t *testing.T) {
	seen := make(chan socks4Request, 1)
	host, port := fakeSOCKS4(t, 91, seen)
	proxy := Manual{Type: TypeSOCKS4, Host: host, Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := dialSOCKS4(ctx, &net.Dialer{}, proxy, "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("dial error = %v, want a rejection", err)
	}
	if _, err := dialSOCKS4(ctx, &net.Dialer{}, proxy, "tcp", "[2001:db8::1]:443"); err == nil {
		t.Fatal("SOCKS4 must refuse an IPv6 target")
	}
}

func TestManualSOCKS4ProxyHonorsCancellation(t *testing.T) {
	host, port := serveProxy(t, func(conn net.Conn) {
		// Never answer, so only the context can end the handshake.
		_, _ = io.Copy(io.Discard, conn)
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := dialSOCKS4(ctx, &net.Dialer{}, Manual{Type: TypeSOCKS4, Host: host, Port: port}, "tcp", "127.0.0.1:80")
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dial error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a canceled SOCKS4 handshake did not return")
	}
}
