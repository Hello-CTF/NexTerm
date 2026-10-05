//go:build windows

package ssh

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func windowsConnectexError(errno windows.Errno) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connectex", Err: errno}}
}

func TestClassifyErrorKindWindowsWSA(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind ErrorKind
	}{
		{"refused", windowsConnectexError(windows.WSAECONNREFUSED), ErrorKindRefused},
		{"host unreachable", windowsConnectexError(windows.WSAEHOSTUNREACH), ErrorKindUnreachable},
		{"network unreachable", windowsConnectexError(windows.WSAENETUNREACH), ErrorKindUnreachable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, ok := classifyErrorKind(test.err)
			if !ok || kind != test.kind {
				t.Fatalf("classifyErrorKind(%v) = %q, %v; want %q, true", test.err, kind, ok, test.kind)
			}
		})
	}
}

func TestClassifyDialErrorWindowsRefused(t *testing.T) {
	connectErr := classifyDialError("127.0.0.1", 22, windowsConnectexError(windows.WSAECONNREFUSED))
	if connectErr.Kind != ErrorKindRefused || connectErr.Op != "dial" || connectErr.Host != "127.0.0.1" || connectErr.Port != 22 {
		t.Fatalf("direct dial classification = %+v", connectErr)
	}

	proxied := &proxyError{endpoint: "socks5://127.0.0.1:1080", err: fmt.Errorf("SOCKS5 connect: %w", windowsConnectexError(windows.WSAECONNREFUSED))}
	connectErr = classifyDialError("203.0.113.10", 2222, proxied)
	if connectErr.Kind != ErrorKindRefused || connectErr.Op != "proxy connect" {
		t.Fatalf("proxied dial classification = %+v", connectErr)
	}
	if connectErr.Proxy != "socks5://127.0.0.1:1080" || connectErr.Host != "203.0.113.10" || connectErr.Port != 2222 {
		t.Fatalf("proxied dial context = %+v", connectErr)
	}
	if !strings.Contains(connectErr.Hint, "proxy") {
		t.Fatalf("proxied dial hint = %q", connectErr.Hint)
	}
}
