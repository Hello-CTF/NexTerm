package remotedesktop

import (
	"fmt"
	"syscall"
	"testing"
)

func TestWSACodeHostUnreachable(t *testing.T) {
	err := fmt.Errorf("dial tcp 192.0.2.1:3389: %w", syscall.EHOSTUNREACH)
	if got := wsaCode(err); got != wsaHostUnreachable {
		t.Fatalf("wsaCode() = %d, want %d", got, wsaHostUnreachable)
	}
}

func TestWSACodeWinsockErrno(t *testing.T) {
	cases := []struct {
		errno syscall.Errno
		want  uint16
	}{
		{10061, wsaConnRefused},
		{10060, wsaTimedOut},
		{10065, wsaHostUnreachable},
		{10051, wsaHostUnreachable},
	}
	for _, c := range cases {
		err := fmt.Errorf("dial tcp 192.0.2.1:3389: connectex: %w", c.errno)
		if got := wsaCode(err); got != c.want {
			t.Errorf("wsaCode(errno %d) = %d, want %d", c.errno, got, c.want)
		}
	}
}
