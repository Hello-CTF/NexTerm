package aicontext

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type reconTransport struct {
	mu    sync.Mutex
	calls []string
}

func (t *reconTransport) Kind() string       { return "fake" }
func (t *reconTransport) Generation() uint64 { return 9 }
func (t *reconTransport) Exec(_ context.Context, command string, options base.ExecOptions) (base.ExecResult, error) {
	t.mu.Lock()
	t.calls = append(t.calls, command)
	t.mu.Unlock()
	time.Sleep(time.Millisecond)
	if options.ExpectedGeneration != 9 {
		return base.ExecResult{}, base.ErrStaleGeneration
	}
	return base.ExecResult{Stdout: "ok\n"}, nil
}
func (t *reconTransport) Ping(context.Context) (time.Duration, error) { return 0, nil }
func (t *reconTransport) IsAlive() bool                               { return true }
func (t *reconTransport) Close() error                                { return nil }
func (t *reconTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.calls)
}

func TestBuilderAssemblesAndCachesVolatileContext(t *testing.T) {
	transport := &reconTransport{}
	now := time.Unix(0, 0)
	builder := NewBuilder(Dependencies{
		Session: func(context.Context, string) (SessionBrief, error) {
			return SessionBrief{Name: "web", Kind: "ssh", Host: "host:22"}, nil
		},
		Screen: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: "visible", Tail: []string{"tail"}}, nil
		},
		Tail: func(context.Context, string, int) ([]string, error) { return []string{"recent"}, nil },
		Tables: func(context.Context, string) ([]TableBrief, error) {
			return []TableBrief{{Schema: "app", Name: "users"}}, nil
		},
		Containers: func(context.Context, string) ([]tools.Container, error) {
			return []tools.Container{{Name: "web", State: "running"}}, nil
		},
		Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		Now:       func() time.Time { return now }, ReconTTL: time.Minute,
	})
	scope := tools.Scope{SessionID: "s", TabID: "t", ConnID: "c"}
	first := builder.Build(context.Background(), scope, "selected")
	for _, expected := range []string{"[会话上下文]", "[终端当前屏幕]", "visible", "recent", "[环境侦察快照]", "app.users", "web [running]", "selected"} {
		if !strings.Contains(first.Volatile, expected) {
			t.Errorf("missing %q in volatile context", expected)
		}
	}
	if transport.count() != len(reconCommands) {
		t.Fatalf("recon calls=%d", transport.count())
	}
	second := builder.Build(context.Background(), scope, "selected")
	if transport.count() != len(reconCommands) || first.Stable != second.Stable {
		t.Fatalf("cache calls=%d stable mismatch", transport.count())
	}
	now = now.Add(2 * time.Minute)
	_ = builder.Build(context.Background(), scope, "selected")
	if transport.count() != 2*len(reconCommands) {
		t.Fatalf("expired cache calls=%d", transport.count())
	}
}

func TestBuilderReconSingleFlight(t *testing.T) {
	transport := &reconTransport{}
	builder := NewBuilder(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	const count = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = builder.Build(context.Background(), tools.Scope{SessionID: "s"}, "")
		}()
	}
	close(start)
	wg.Wait()
	if transport.count() != len(reconCommands) {
		t.Fatalf("recon executed %d commands, want %d", transport.count(), len(reconCommands))
	}
}
