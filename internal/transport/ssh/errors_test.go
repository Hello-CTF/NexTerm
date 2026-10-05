package ssh

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestClassifyErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind ErrorKind
		ok   bool
	}{
		{"canceled", context.Canceled, ErrorKindCanceled, true},
		{"deadline", context.DeadlineExceeded, ErrorKindTimeout, true},
		{"dns not found", &net.DNSError{Err: "no such host", Name: "missing.test", IsNotFound: true}, ErrorKindDNS, true},
		{"dns timeout", &net.DNSError{Err: "timeout", Name: "slow.test", IsTimeout: true}, ErrorKindTimeout, true},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, ErrorKindRefused, true},
		{"refused wrapped", &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}, ErrorKindRefused, true},
		{"host unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}, ErrorKindUnreachable, true},
		{"network unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}, ErrorKindUnreachable, true},
		{"net timeout", &net.OpError{Op: "read", Net: "tcp", Err: timeoutError{}}, ErrorKindTimeout, true},
		{"plain", errors.New("boom"), "", false},
		{"nil", nil, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, ok := classifyErrorKind(test.err)
			if kind != test.kind || ok != test.ok {
				t.Fatalf("classifyErrorKind(%v) = %q, %v; want %q, %v", test.err, kind, ok, test.kind, test.ok)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func closedTCPPort(t *testing.T) int {
	t.Helper()
	listener := newTCPListener(t)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func stallingTCPListener(t *testing.T) net.Listener {
	t.Helper()
	listener := newTCPListener(t)
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		listener.Close()
	})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				connection.Close()
			}()
		}
	}()
	return listener
}

func requireConnectError(t *testing.T, err error, kind ErrorKind) *ConnectError {
	t.Helper()
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("error %v (%T) is not a ConnectError", err, err)
	}
	if connectErr.Kind != kind {
		t.Fatalf("ConnectError.Kind = %q, want %q: %v", connectErr.Kind, kind, connectErr)
	}
	if connectErr.Hint == "" {
		t.Fatalf("ConnectError %q has an empty hint: %v", connectErr.Kind, connectErr)
	}
	if connectErr.Op == "" || connectErr.Err == nil {
		t.Fatalf("ConnectError missing op or cause: %+v", connectErr)
	}
	return connectErr
}

func TestConnectErrorClassificationRefused(t *testing.T) {
	cfg := Config{
		Host: "127.0.0.1", Port: closedTCPPort(t), User: "test",
		Auth:     AuthConfig{Method: AuthPassword, Password: "secret"},
		HostKeys: NewMemoryHostKeyStore(), ConnectTimeout: 2 * time.Second,
	}
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Op != "dial" || connectErr.Host != "127.0.0.1" || connectErr.Port != cfg.Port || connectErr.Proxy != "" {
		t.Fatalf("refused dial context = %+v", connectErr)
	}
}

func TestConnectErrorClassificationDNS(t *testing.T) {
	cfg := Config{
		Host: "nonexistent.invalid.", Port: 22, User: "test",
		Auth:     AuthConfig{Method: AuthPassword, Password: "secret"},
		HostKeys: NewMemoryHostKeyStore(), ConnectTimeout: 5 * time.Second,
	}
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindDNS)
	if connectErr.Op != "dial" {
		t.Fatalf("dns op = %q", connectErr.Op)
	}
}

func TestConnectErrorClassificationTimeout(t *testing.T) {
	listener := stallingTCPListener(t)
	cfg := Config{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "test",
		Auth:     AuthConfig{Method: AuthPassword, Password: "secret"},
		HostKeys: NewMemoryHostKeyStore(), ConnectTimeout: 300 * time.Millisecond,
	}
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindTimeout)
	if connectErr.Op != "handshake" {
		t.Fatalf("timeout op = %q", connectErr.Op)
	}
}

func TestConnectErrorClassificationAuth(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "wrong-password"})
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Op != "authentication" {
		t.Fatalf("auth op = %q", connectErr.Op)
	}
	if strings.Contains(connectErr.Error(), "wrong-password") {
		t.Fatalf("auth error leaked the password: %v", connectErr)
	}
}

func TestConnectErrorClassificationAuthSetup(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthKey, KeyPEM: []byte("not a key")})
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindAuth)
	if connectErr.Op != "authentication setup" {
		t.Fatalf("auth setup op = %q", connectErr.Op)
	}

	cfg = testClientConfig(t, server, AuthConfig{Method: AuthAgent})
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err = Connect(context.Background(), cfg)
	requireConnectError(t, err, ErrorKindAuth)
}

func TestConnectErrorClassificationHostKey(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.AutoAcceptUnknown = false
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindHostKeyPending)
	if !errors.Is(err, ErrHostKeyPending) {
		t.Fatalf("pending host key lost sentinel: %v", err)
	}
	var keyErr *HostKeyError
	if !errors.As(err, &keyErr) || !keyErr.Pending {
		t.Fatalf("pending host key lost HostKeyError: %v", err)
	}
	presented := newHostKey(cfg.Host, cfg.Port, server.hostKey)
	cfg.HostKeyApproval = &HostKeyApproval{Fingerprint: presented.Fingerprint}
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	if err := cfg.HostKeys.PutHostKey(context.Background(), newHostKey(cfg.Host, cfg.Port, hostKeyForTest(t)), true); err != nil {
		t.Fatal(err)
	}
	_, err = Connect(context.Background(), cfg)
	connectErr = requireConnectError(t, err, ErrorKindHostKeyChanged)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("changed host key lost sentinel: %v", err)
	}
	if connectErr.Proxy != "" {
		t.Fatalf("direct connect reported proxy %q", connectErr.Proxy)
	}
}

func TestConnectErrorClassificationProxyHops(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.ProxyURL = fmt.Sprintf("socks5://127.0.0.1:%d", closedTCPPort(t))
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Op != "proxy connect" || connectErr.Proxy != cfg.ProxyURL {
		t.Fatalf("proxy hop context = %+v", connectErr)
	}
	if connectErr.Host != cfg.Host || connectErr.Port != cfg.Port {
		t.Fatalf("proxy hop lost target endpoint: %+v", connectErr)
	}
	if !strings.Contains(connectErr.Hint, "proxy") {
		t.Fatalf("proxy hop hint = %q", connectErr.Hint)
	}
}

func TestConnectErrorClassificationHTTPProxyRejection(t *testing.T) {
	listener := newTCPListener(t)
	defer listener.Close()
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		if _, err := http.ReadRequest(bufio.NewReader(connection)); err != nil {
			return
		}
		_, _ = connection.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
	}()
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.ProxyURL = "http://" + listener.Addr().String()
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindProxy)
	if !strings.Contains(connectErr.Error(), "407") {
		t.Fatalf("proxy rejection lost status: %v", connectErr)
	}
}

func TestConnectErrorSanitizesProxyCredentials(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.ProxyURL = "socks5://proxyuser:topsecret@127.0.0.1:1"
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindRefused)
	if connectErr.Proxy != "socks5://127.0.0.1:1" {
		t.Fatalf("sanitized proxy endpoint = %q", connectErr.Proxy)
	}
	message := connectErr.Error()
	if strings.Contains(message, "topsecret") || strings.Contains(message, "proxyuser") {
		t.Fatalf("proxy credentials leaked into error: %v", message)
	}
}

func TestConnectErrorClassificationCanceled(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Connect(ctx, cfg)
	requireConnectError(t, err, ErrorKindCanceled)
}

func TestConnectErrorClassificationProxyConfig(t *testing.T) {
	server := newTestSSHServer(t, nil)

	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.ProxyURL = "http://proxyuser:topsecret@%"
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindProxy)
	if connectErr.Op != "proxy connect" || connectErr.Proxy != "" {
		t.Fatalf("parse-invalid proxy context = %+v", connectErr)
	}
	message := connectErr.Error()
	if strings.Contains(message, "topsecret") || strings.Contains(message, "proxyuser") || strings.Contains(message, "%") {
		t.Fatalf("parse-invalid proxy leaked the raw URL: %v", message)
	}

	cfg.ProxyURL = "socks5://"
	_, err = Connect(context.Background(), cfg)
	connectErr = requireConnectError(t, err, ErrorKindProxy)
	if connectErr.Op != "proxy connect" || connectErr.Proxy != "" {
		t.Fatalf("missing-host proxy context = %+v", connectErr)
	}

	cfg.ProxyURL = "socks5://proxyuser:topsecret@"
	_, err = Connect(context.Background(), cfg)
	connectErr = requireConnectError(t, err, ErrorKindProxy)
	if connectErr.Proxy != "" {
		t.Fatalf("missing-host proxy endpoint = %q", connectErr.Proxy)
	}
	if strings.Contains(connectErr.Error(), "topsecret") || strings.Contains(connectErr.Error(), "proxyuser") {
		t.Fatalf("missing-host proxy leaked userinfo: %v", connectErr)
	}
}

func TestConnectErrorClassificationConfig(t *testing.T) {
	cfg := Config{Host: " ", Port: 22, User: "test", HostKeys: NewMemoryHostKeyStore()}
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindConfig)
	if connectErr.Op != "configuration" {
		t.Fatalf("config op = %q", connectErr.Op)
	}
}

func TestConnectErrorJumpConfigFailureNamesTheFailingHop(t *testing.T) {
	store := NewMemoryHostKeyStore()

	jumpCfg := Config{Host: "jump.example", Port: 70000, User: "test", HostKeys: store}
	cfg := Config{Host: "target.example", Port: 22, User: "test", HostKeys: store, Jump: &jumpCfg}
	_, err := Connect(context.Background(), cfg)
	connectErr := requireConnectError(t, err, ErrorKindConfig)
	if connectErr.Host != "jump.example" || connectErr.Port != 70000 {
		t.Fatalf("jump port config failure endpoint = %+v", connectErr)
	}

	jumpCfg = Config{Host: "jump.example", Port: 0, User: " ", HostKeys: store}
	cfg.Jump = &jumpCfg
	_, err = Connect(context.Background(), cfg)
	connectErr = requireConnectError(t, err, ErrorKindConfig)
	if connectErr.Host != "jump.example" || connectErr.Port != 22 {
		t.Fatalf("jump user config failure endpoint = %+v", connectErr)
	}

	innerCfg := Config{Host: "inner-jump.example", Port: 70000, User: "test", HostKeys: store}
	jumpCfg = Config{Host: "jump.example", Port: 22, User: "test", HostKeys: store, Jump: &innerCfg}
	cfg.Jump = &jumpCfg
	_, err = Connect(context.Background(), cfg)
	connectErr = requireConnectError(t, err, ErrorKindConfig)
	if connectErr.Host != "inner-jump.example" || connectErr.Port != 70000 {
		t.Fatalf("nested jump config failure endpoint = %+v", connectErr)
	}
}
