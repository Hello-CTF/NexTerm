package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

func TestExpandProxyCommand(t *testing.T) {
	cases := []struct {
		command string
		host    string
		port    int
		want    string
	}{
		{"nc %h %p", "example.test", 2222, "nc example.test 2222"},
		{"%%h %%p %h %p", "h", 22, "%h %p h 22"},
		{"%h%p", "host", 22, "host22"},
		{"100%% ready %p", "x", 2200, "100% ready 2200"},
		{"plain command", "ignored", 22, "plain command"},
	}
	for _, testCase := range cases {
		if got := expandProxyCommand(testCase.command, testCase.host, testCase.port); got != testCase.want {
			t.Fatalf("expandProxyCommand(%q, %q, %d) = %q, want %q", testCase.command, testCase.host, testCase.port, got, testCase.want)
		}
	}
}

func TestSSHProxyCommandConnect(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	cfg.ProxyCommand = proxyHelperCommand()
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHProxyCommandCompositionWithJump(t *testing.T) {
	jump := newTestSSHServer(t, nil)
	target := newTestSSHServer(t, nil)
	store := NewMemoryHostKeyStore()
	auth := AuthConfig{Method: AuthPassword, Password: "secret"}
	jumpCfg := testClientConfig(t, jump, auth)
	jumpCfg.HostKeys = store
	jumpCfg.ProxyCommand = proxyHelperCommand()
	targetCfg := testClientConfig(t, target, auth)
	targetCfg.HostKeys = store
	targetCfg.Jump = &jumpCfg
	client, err := Connect(context.Background(), targetCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, server := range []*testSSHServer{jump, target} {
		host, port := splitAddress(t, server.listener.Addr().String())
		keys, err := store.HostKeys(context.Background(), host, port)
		if err != nil || len(keys) != 1 {
			t.Fatalf("host keys for hop %s:%d = %+v, %v", host, port, keys, err)
		}
	}
}

func TestProxyCommandAndProxyURLAreMutuallyExclusive(t *testing.T) {
	cfg := Config{
		Host: "example.test", User: "test", HostKeys: NewMemoryHostKeyStore(),
		ProxyCommand: "nc %h %p", ProxyURL: "socks5://127.0.0.1:1080",
	}
	if _, err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("validate = %v", err)
	}
}

func TestProxyCommandCancelledContextDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialProxyCommand(ctx, "cat", "example.test", 22); err == nil {
		t.Fatal("cancelled context started a proxy command")
	}
}

func TestProxyCommandLineKeepsPayloadVerbatim(t *testing.T) {
	cases := []struct {
		name       string
		executable string
		command    string
		host       string
		port       int
		want       string
	}{
		{
			name:       "plain command",
			executable: `C:\Windows\System32\cmd.exe`,
			command:    "nc %h %p",
			host:       "example.test",
			port:       2222,
			want:       `"C:\Windows\System32\cmd.exe" /C nc example.test 2222`,
		},
		{
			name:       "executable path with spaces",
			executable: `C:\Program Files\Utility\cmd.exe`,
			command:    "nc %h %p",
			host:       "10.0.0.1",
			port:       22,
			want:       `"C:\Program Files\Utility\cmd.exe" /C nc 10.0.0.1 22`,
		},
		{
			name:       "quoted set and quoted executable stay untouched",
			executable: `C:\Windows\System32\cmd.exe`,
			command:    `set "NEXTERM_SSH_PROXY_HELPER=1" && "C:\a b\ssh.test.exe" -test.run=^TestX$ %h %p`,
			host:       "127.0.0.1",
			port:       2201,
			want:       `"C:\Windows\System32\cmd.exe" /C set "NEXTERM_SSH_PROXY_HELPER=1" && "C:\a b\ssh.test.exe" -test.run=^TestX$ 127.0.0.1 2201`,
		},
		{
			name:       "cmd special characters are not escaped",
			executable: `C:\Windows\System32\cmd.exe`,
			command:    `echo 100%% & echo a^|b>c<d %p`,
			host:       "h",
			port:       22,
			want:       `"C:\Windows\System32\cmd.exe" /C echo 100% & echo a^|b>c<d 22`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			line := proxyCommandLine(testCase.executable, expandProxyCommand(testCase.command, testCase.host, testCase.port))
			if line != testCase.want {
				t.Fatalf("command line = %q, want %q", line, testCase.want)
			}
		})
	}
}

func TestSSHProxyCommandHelper(t *testing.T) {
	if os.Getenv("NEXTERM_SSH_PROXY_HELPER") != "1" {
		t.Skip("helper process for proxy command tests")
	}
	args := os.Args
	host, port := args[len(args)-2], args[len(args)-1]
	conn, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(os.Stdout, conn)
	os.Exit(0)
}
