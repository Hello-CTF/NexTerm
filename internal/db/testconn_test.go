package db

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

const testconnSecret = "testconn-secret-password"

func testconnService(asset Asset) *Service {
	return NewService(AssetResolverFunc(func(context.Context, string) (Asset, error) { return asset, nil }))
}

func testconnAsset(kind string, port int, options string) Asset {
	host, username := "127.0.0.1", "app"
	return Asset{Kind: kind, Host: &host, Port: &port, Username: &username, Password: testconnSecret, OptionsJSON: options}
}

func closedTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func describeAsset(t *testing.T, service *Service, kind string, timeout time.Duration) (string, string) {
	t.Helper()
	err := service.TestAsset(context.Background(), "asset", timeout)
	if err == nil {
		t.Fatal("TestAsset succeeded, want failure")
	}
	kindName, message := DescribeTestFailure(err, kind, "127.0.0.1")
	if strings.Contains(message, testconnSecret) {
		t.Fatalf("message leaks the password: %q", message)
	}
	return kindName, message
}

func TestTestAssetClosesWithoutRegisteringHandle(t *testing.T) {
	service := testconnService(testconnAsset("redis", 6379, ""))
	closed := 0
	service.openRedis = func(context.Context, RedisConfig) (*redisConnection, error) {
		return &redisConnection{client: &fakeRedis{}, console: &fakeRedis{}, closeFn: func() error { closed++; return nil }}, nil
	}
	if err := service.TestAsset(context.Background(), "asset", time.Second); err != nil {
		t.Fatalf("TestAsset: %v", err)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if closed != 1 || len(service.conns) != 0 || len(service.owners) != 0 {
		t.Fatalf("closed=%d conns=%d owners=%d", closed, len(service.conns), len(service.owners))
	}
}

func TestTestAssetHonoursTLSSettingsWithoutOverriding(t *testing.T) {
	service := testconnService(testconnAsset("redis", 6379, `{"tls":{"mode":"required"}}`))
	var captured RedisConfig
	service.openRedis = func(_ context.Context, config RedisConfig) (*redisConnection, error) {
		captured = config
		return &redisConnection{client: &fakeRedis{}, console: &fakeRedis{}}, nil
	}
	if err := service.TestAsset(context.Background(), "asset", time.Second); err != nil {
		t.Fatal(err)
	}
	if captured.TLSConfig == nil || captured.TLSConfig.InsecureSkipVerify {
		t.Fatalf("test must keep certificate verification on: %+v", captured.TLSConfig)
	}
}

func TestTestAssetClosedPortIsRefusedForEveryKind(t *testing.T) {
	for _, kind := range []string{"mysql", "postgres", "redis"} {
		t.Run(kind, func(t *testing.T) {
			service := testconnService(testconnAsset(kind, closedTCPPort(t), ""))
			got, message := describeAsset(t, service, kind, 2*time.Second)
			if got != TestKindRefused || !strings.Contains(message, "拒绝了连接") {
				t.Fatalf("kind=%q message=%q", got, message)
			}
		})
	}
}

func TestTestAssetSilentServerTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { <-release; _ = conn.Close() }()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	for _, kind := range []string{"mysql", "postgres", "redis"} {
		t.Run(kind, func(t *testing.T) {
			started := time.Now()
			got, _ := describeAsset(t, testconnService(testconnAsset(kind, port, "")), kind, 300*time.Millisecond)
			if got != TestKindTimeout {
				t.Fatalf("kind=%q", got)
			}
			if time.Since(started) > 5*time.Second {
				t.Fatalf("test ran %s, timeout not honoured", time.Since(started))
			}
		})
	}
}

func TestTestAssetRedisWrongPasswordIsAuthFailure(t *testing.T) {
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
				// Answer every RESP command with the same error; a command header is a "*<n>" line.
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(line, "*") {
						_, _ = conn.Write([]byte("-WRONGPASS invalid username-password pair or user is disabled.\r\n"))
					}
				}
			}()
		}
	}()
	service := testconnService(testconnAsset("redis", listener.Addr().(*net.TCPAddr).Port, ""))
	got, message := describeAsset(t, service, "redis", 2*time.Second)
	if got != TestKindAuth || !strings.Contains(message, "凭据") {
		t.Fatalf("kind=%q message=%q", got, message)
	}
}

func TestTestAssetUntrustedCertificateIsTLSFailureNotAutoTrusted(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	server.TLS = &tls.Config{}
	server.StartTLS()
	t.Cleanup(server.Close)
	port, err := strconv.Atoi(server.Listener.Addr().String()[strings.LastIndex(server.Listener.Addr().String(), ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	service := testconnService(testconnAsset("redis", port, `{"tls":{"mode":"required"}}`))
	got, message := describeAsset(t, service, "redis", 2*time.Second)
	if got != TestKindTLS || !strings.Contains(message, "不会自动信任") {
		t.Fatalf("kind=%q message=%q", got, message)
	}
}

func TestDescribeTestFailureTLSErrorShapes(t *testing.T) {
	for name, err := range map[string]error{
		"unknown authority": x509.UnknownAuthorityError{},
		"hostname":          x509.HostnameError{Host: "db.internal"},
		"verification":      &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		"mysql no tls":      mysqlDriver.ErrNoTLS,
		"wrapped":           ipc.WrapError(ipc.CodeInternal, "x", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}),
		"postgres refused":  errors.New("failed to connect: server refused TLS connection"),
	} {
		t.Run(name, func(t *testing.T) {
			if kind, _ := DescribeTestFailure(err, "postgres", ""); kind != TestKindTLS {
				t.Fatalf("kind=%q", kind)
			}
		})
	}
}

func TestDescribeTestFailureClassifiesDriverAndIPCErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind string
	}{
		{"mysql denied", ipc.WrapError(ipc.CodeInternal, "x", &mysqlDriver.MySQLError{Number: 1045, Message: testconnSecret}), TestKindAuth},
		{"mysql unknown db", &mysqlDriver.MySQLError{Number: 1049}, TestKindDatabase},
		{"postgres password", &pgconn.PgError{Code: "28P01", Message: testconnSecret}, TestKindAuth},
		{"postgres missing db", &pgconn.PgError{Code: "3D000"}, TestKindDatabase},
		{"vault locked", ipc.NewError(ipc.CodeVaultLocked, "locked"), TestKindVault},
		{"missing asset", ipc.NewError(ipc.CodeNotFound, "gone"), TestKindNotFound},
		{"bad config", ipc.NewError(ipc.CodeBadParam, "bad"), TestKindConfig},
		{"tunnel down", ipc.NewError(ipc.CodeDisconnected, "no session"), TestKindTunnel},
		{"deadline", context.DeadlineExceeded, TestKindTimeout},
		{"canceled", context.Canceled, TestKindCanceled},
		{"dns", &net.DNSError{Name: "nope.invalid", IsNotFound: true}, TestKindDNS},
		{"eof", io.ErrUnexpectedEOF, TestKindNetwork},
		{"unknown", errors.New("boom " + testconnSecret), TestKindInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, message := DescribeTestFailure(tc.err, "mysql", "db.internal:3306")
			if kind != tc.kind || message == "" || strings.Contains(message, testconnSecret) {
				t.Fatalf("kind=%q message=%q, want %q", kind, message, tc.kind)
			}
		})
	}
	if kind, message := DescribeTestFailure(nil, "mysql", ""); kind != "" || message != "" {
		t.Fatalf("nil error = %q %q", kind, message)
	}
}
