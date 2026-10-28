package device

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Exec must honour the same stale-generation and closed-transport guards as
// OpenExec: a command issued for an earlier connection must never be dialled
// and run through a replacement or closed transport.
func TestExecRejectsStaleGenerationAndClosedTransportWithoutDialing(t *testing.T) {
	var dials atomic.Int32
	dial := func(context.Context, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("dial must not happen")
	}
	transport := New("dev-1", "digest", dial, 2)
	if _, err := transport.Exec(context.Background(), "true", base.ExecOptions{ExpectedGeneration: 1}); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("stale generation err=%v, want ErrDisconnected", err)
	}
	_ = transport.Close()
	if _, err := transport.Exec(context.Background(), "true", base.ExecOptions{}); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("closed transport err=%v, want ErrClosed", err)
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("Exec dialled %d bridges despite the guards", n)
	}
}
