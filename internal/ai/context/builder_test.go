package aicontext

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type reconTransport struct {
	mu     sync.Mutex
	calls  []string
	output string
	err    error
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
	if t.err != nil {
		return base.ExecResult{}, t.err
	}
	output := t.output
	if output == "" {
		output = "ok\n"
	}
	return base.ExecResult{Stdout: output}, nil
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
	for _, expected := range []string{"[Session Context]", "[Current Terminal Screen]", "visible", "recent", "[Environment Recon Snapshot]", "app.users", "web [running]", "selected"} {
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
	var resolverMu sync.Mutex
	resolverCalls := 0
	builder := NewBuilder(Dependencies{Transport: func(context.Context, string) (base.Transport, error) {
		resolverMu.Lock()
		resolverCalls++
		resolverMu.Unlock()
		return transport, nil
	}})
	const count = 8
	start := make(chan struct{})
	owner := ipc.WithUserID(context.Background(), "owner")
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = builder.Build(owner, tools.Scope{SessionID: "s"}, "")
		}()
	}
	close(start)
	wg.Wait()
	if transport.count() != len(reconCommands) {
		t.Fatalf("recon executed %d commands, want %d", transport.count(), len(reconCommands))
	}
	resolverMu.Lock()
	defer resolverMu.Unlock()
	if resolverCalls != count {
		t.Fatalf("transport authorization ran %d times, want %d", resolverCalls, count)
	}
}

func TestSessionContextDependenciesEnforceAccountOwnership(t *testing.T) {
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &reconTransport{}, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	owner := ipc.WithUserID(context.Background(), "owner")
	other := ipc.WithUserID(context.Background(), "other")
	connected, err := manager.Connect(owner, session.Asset{ID: "owner-ssh", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.RegisterRemoteSession(owner, session.RemoteSessionOptions{OwnerID: "owner", Title: "owner-tab"})
	if err != nil {
		t.Fatal(err)
	}
	deps := WithSession(Dependencies{}, manager, nil, nil)
	if _, err := deps.Session(owner, connected.ID); err != nil {
		t.Fatalf("owner session = %v", err)
	}
	if _, err := deps.Session(other, connected.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account session = %v, want ErrForbidden", err)
	}
	if _, err := deps.Transport(other, connected.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account transport = %v, want ErrForbidden", err)
	}
	if _, err := deps.Screen(other, tab.ID); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account screen = %v, want ErrForbidden", err)
	}
	if _, err := deps.Tail(other, tab.ID, 10); !errors.Is(err, session.ErrForbidden) {
		t.Errorf("cross-account tail = %v, want ErrForbidden", err)
	}
	if _, err := deps.Tail(owner, tab.ID, 10); err != nil {
		t.Fatalf("owner tail = %v", err)
	}

	legacyTabSession := deps.TabSession
	legacyCalls := 0
	deps.TabSession = func(tabID string) string {
		legacyCalls++
		return legacyTabSession(tabID)
	}
	builder := NewBuilder(deps)
	scope := tools.Scope{SessionID: tab.SessionID, TabID: tab.ID}
	if err := manager.FeedRemote(owner, tab.ID, []byte("owner-context-marker")); err != nil {
		t.Fatal(err)
	}
	foreign := builder.Build(other, scope, "")
	if foreign.Bundle.Screen != "" || len(foreign.Bundle.Tail) != 0 || strings.Contains(foreign.Volatile, "owner-context-marker") {
		t.Fatalf("cross-account prompt leaked terminal data: %+v", foreign.Bundle)
	}
	if legacyCalls != 0 {
		t.Fatalf("cross-account build used unscoped TabSession %d times", legacyCalls)
	}
	owned := builder.Build(owner, scope, "")
	if !strings.Contains(owned.Volatile, "owner-context-marker") {
		t.Fatalf("owner prompt missing terminal marker: %q", owned.Volatile)
	}
	open := builder.Build(context.Background(), scope, "")
	if !strings.Contains(open.Volatile, "owner-context-marker") {
		t.Fatalf("trusted open prompt missing terminal marker: %q", open.Volatile)
	}
}

func TestBuilderReconCacheRequiresScopedTransportAndIsolatesOwners(t *testing.T) {
	transports := map[string]*reconTransport{
		"owner":   {output: "owner-recon-marker\n"},
		"other":   {output: "other-recon-marker\n"},
		"":        {output: "open-recon-marker\n"},
		"foreign": {output: "recovered-recon-marker\n"},
	}
	resolverCalls := map[string]int{}
	foreignAllowed := false
	builder := NewBuilder(Dependencies{Transport: func(ctx context.Context, _ string) (base.Transport, error) {
		owner := ipc.OwnerID(ctx)
		resolverCalls[owner]++
		if owner == "foreign" && !foreignAllowed {
			return nil, errors.New("foreign transport denied")
		}
		return transports[owner], nil
	}})
	scope := tools.Scope{SessionID: "shared-session"}
	owner := ipc.WithUserID(context.Background(), "owner")
	other := ipc.WithUserID(context.Background(), "other")
	foreign := ipc.WithUserID(context.Background(), "foreign")

	first := builder.Build(owner, scope, "")
	second := builder.Build(owner, scope, "")
	if !strings.Contains(first.Bundle.Recon, "owner-recon-marker") || !strings.Contains(second.Bundle.Recon, "owner-recon-marker") {
		t.Fatalf("owner recon missing: first=%q second=%q", first.Bundle.Recon, second.Bundle.Recon)
	}
	if resolverCalls["owner"] != 2 {
		t.Errorf("cache hit skipped transport authorization: calls=%d", resolverCalls["owner"])
	}
	if transports["owner"].count() != len(reconCommands) {
		t.Errorf("owner cache executed %d commands, want %d", transports["owner"].count(), len(reconCommands))
	}

	otherPrompt := builder.Build(other, scope, "")
	if !strings.Contains(otherPrompt.Bundle.Recon, "other-recon-marker") || strings.Contains(otherPrompt.Bundle.Recon, "owner-recon-marker") {
		t.Errorf("different owner received wrong recon: %q", otherPrompt.Bundle.Recon)
	}
	openPrompt := builder.Build(context.Background(), scope, "")
	if !strings.Contains(openPrompt.Bundle.Recon, "open-recon-marker") || strings.Contains(openPrompt.Bundle.Recon, "owner-recon-marker") {
		t.Errorf("ownerless request received wrong recon: %q", openPrompt.Bundle.Recon)
	}
	cachedOpen := builder.Build(context.Background(), scope, "")
	if !strings.Contains(cachedOpen.Bundle.Recon, "open-recon-marker") || resolverCalls[""] != 2 {
		t.Errorf("ownerless cache did not reauthorize: recon=%q calls=%d", cachedOpen.Bundle.Recon, resolverCalls[""])
	}
	if transports[""].count() != len(reconCommands) {
		t.Errorf("ownerless cache executed %d commands, want %d", transports[""].count(), len(reconCommands))
	}

	for attempt := 1; attempt <= 2; attempt++ {
		denied := builder.Build(foreign, scope, "")
		if denied.Bundle.Recon != "" {
			t.Errorf("denied attempt %d returned recon %q", attempt, denied.Bundle.Recon)
		}
	}
	if resolverCalls["foreign"] != 2 {
		t.Errorf("denied requests did not reach scoped transport: calls=%d", resolverCalls["foreign"])
	}
	foreignAllowed = true
	recovered := builder.Build(foreign, scope, "")
	if !strings.Contains(recovered.Bundle.Recon, "recovered-recon-marker") {
		t.Fatalf("failed authorization was cached: %q", recovered.Bundle.Recon)
	}
}

func TestBuilderReconTransportFailureIsNotCached(t *testing.T) {
	transport := &reconTransport{output: "recovered-after-restart\n"}
	resolverCalls := 0
	builder := NewBuilder(Dependencies{Transport: func(context.Context, string) (base.Transport, error) {
		resolverCalls++
		if resolverCalls == 1 {
			return nil, errors.New("session temporarily unavailable")
		}
		return transport, nil
	}})
	ctx := ipc.WithUserID(context.Background(), "owner")
	scope := tools.Scope{SessionID: "session"}
	if prompt := builder.Build(ctx, scope, ""); prompt.Bundle.Recon != "" {
		t.Fatalf("failed transport returned recon %q", prompt.Bundle.Recon)
	}
	prompt := builder.Build(ctx, scope, "")
	if resolverCalls != 2 || !strings.Contains(prompt.Bundle.Recon, "recovered-after-restart") || transport.count() != len(reconCommands) {
		t.Fatalf("retry did not bypass failed cache: calls=%d commands=%d recon=%q", resolverCalls, transport.count(), prompt.Bundle.Recon)
	}
}

func TestBuilderReconExecFailureIsNotCached(t *testing.T) {
	transport := &reconTransport{output: "recovered-after-exec-error\n", err: errors.New("exec unavailable")}
	builder := NewBuilder(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	ctx := ipc.WithUserID(context.Background(), "owner")
	scope := tools.Scope{SessionID: "session"}
	failed := builder.Build(ctx, scope, "")
	if !strings.Contains(failed.Bundle.Recon, "recon command failed: exec unavailable") {
		t.Fatalf("failed execution did not return safe error text: %q", failed.Bundle.Recon)
	}
	transport.err = nil
	recovered := builder.Build(ctx, scope, "")
	if !strings.Contains(recovered.Bundle.Recon, "recovered-after-exec-error") {
		t.Fatalf("execution failure was cached: %q", recovered.Bundle.Recon)
	}
	if got, want := transport.count(), 2*len(reconCommands); got != want {
		t.Fatalf("recovery executed %d commands, want %d", got, want)
	}
}
