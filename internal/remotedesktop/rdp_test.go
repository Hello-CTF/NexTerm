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
