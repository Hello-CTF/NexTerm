//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type terminalListClock struct{ ms atomic.Int64 }

func (c *terminalListClock) now() time.Time { return time.UnixMilli(c.ms.Load()) }

type terminalListState struct {
	*terminal.Tab
	snapshotCalls atomic.Int64
}

func (s *terminalListState) Snapshot() terminal.Snapshot {
	s.snapshotCalls.Add(1)
	return s.Tab.Snapshot()
}

type terminalListFactory struct {
	clock *terminalListClock
	state *terminalListState
}

func (f *terminalListFactory) NewTerminal(config session.TerminalConfig) (session.TerminalState, error) {
	tab := terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), terminal.UTF8, terminal.WithClock(f.clock.now))
	f.state = &terminalListState{Tab: tab}
	return f.state, nil
}

type terminalListChannel struct {
	closed chan struct{}
	once   sync.Once
}

func newTerminalListChannel() *terminalListChannel {
	return &terminalListChannel{closed: make(chan struct{})}
}

func (c *terminalListChannel) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}
func (c *terminalListChannel) Write(data []byte) (int, error) { return len(data), nil }
func (c *terminalListChannel) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *terminalListChannel) Stderr() io.Reader                            { return nil }
func (c *terminalListChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *terminalListChannel) Wait(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *terminalListChannel) CloseWrite() error  { return nil }
func (c *terminalListChannel) ID() string         { return "terminal-list-channel" }
func (c *terminalListChannel) Generation() uint64 { return 1 }

type terminalListTransport struct {
	channel *terminalListChannel
}

func (t *terminalListTransport) Kind() string       { return session.KindSSH }
func (t *terminalListTransport) Generation() uint64 { return 1 }
func (t *terminalListTransport) IsAlive() bool      { return true }
func (t *terminalListTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *terminalListTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *terminalListTransport) Close() error { return t.channel.Close() }
func (t *terminalListTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	return t.channel, nil
}

func TestTerminalListUsesOutputMetadataWithoutSnapshots(t *testing.T) {
	clock := &terminalListClock{}
	clock.ms.Store(1_000_000)
	channel := newTerminalListChannel()
	factory := &terminalListFactory{clock: clock}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &terminalListTransport{channel: channel}, nil
		}),
		Terminals:  factory,
		OpenAccess: true,
	})
	production, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = channel.Close()
		_ = production.Shutdown(context.Background())
	})
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "terminal-list", Name: "terminal-list", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(t.Context(), session.OpenTabOptions{
		SessionID: connected.ID, Detached: true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	list := func() []liveTabDTO {
		t.Helper()
		response := dispatchDurableTest(t, production, "terminal_list", `null`, "", "client-a")
		var rows []liveTabDTO
		requireStoreTestResponse(t, response, &rows)
		if len(rows) != 1 || rows[0].TabID != tab.ID {
			t.Fatalf("terminal_list rows = %+v, want tab %s", rows, tab.ID)
		}
		var raw []map[string]json.RawMessage
		requireStoreTestResponse(t, response, &raw)
		for _, field := range []string{"text", "lines", "snapshot", "cursorRow", "cursorCol"} {
			if _, ok := raw[0][field]; ok {
				t.Fatalf("lightweight terminal_list contains %s: %s", field, response.Data)
			}
		}
		return rows
	}

	initialSnapshots := factory.state.snapshotCalls.Load()
	initial := list()
	if initial[0].LastOutputAt != 0 || initial[0].LastOutputMSAgo != 0 {
		t.Fatalf("initial output metadata = %+v, want explicit zero values", initial[0])
	}
	if got := factory.state.snapshotCalls.Load(); got != initialSnapshots {
		t.Fatalf("terminal_list generated %d snapshots, want none", got-initialSnapshots)
	}

	clock.ms.Add(10)
	if err := manager.InjectInternal(t.Context(), tab.ID, []byte("first")); err != nil {
		t.Fatal(err)
	}
	first := list()
	if first[0].LastOutputAt != 1_000_010 {
		t.Fatalf("first lastOutputAt = %d, want 1000010", first[0].LastOutputAt)
	}
	clock.ms.Add(10)
	if err := manager.InjectInternal(t.Context(), tab.ID, []byte("second")); err != nil {
		t.Fatal(err)
	}
	second := list()
	if second[0].LastOutputAt != 1_000_020 || second[0].LastOutputAt <= first[0].LastOutputAt {
		t.Fatalf("output timestamps = %d then %d, want advancing values", first[0].LastOutputAt, second[0].LastOutputAt)
	}
	if got := factory.state.snapshotCalls.Load(); got != initialSnapshots {
		t.Fatalf("terminal_list generated snapshots after output: %d", got-initialSnapshots)
	}

	snapshotResponse := dispatchDurableTest(t, production, "terminal_snapshot", `{"tabId":"`+tab.ID+`"}`, "", "client-a")
	var snapshot terminal.Snapshot
	requireStoreTestResponse(t, snapshotResponse, &snapshot)
	if !strings.Contains(snapshot.Text, "second") {
		t.Fatalf("on-demand snapshot text = %q, want latest output", snapshot.Text)
	}
	if got := factory.state.snapshotCalls.Load(); got != initialSnapshots+1 {
		t.Fatalf("on-demand snapshot calls = %d, want one", got-initialSnapshots)
	}
}

func TestTerminalListPreservesLegacyOutputAge(t *testing.T) {
	clock := &terminalListClock{}
	clock.ms.Store(1_000_000)
	channel := newTerminalListChannel()
	factory := &terminalListFactory{clock: clock}
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return &terminalListTransport{channel: channel}, nil
		}),
		Terminals:  factory,
		OpenAccess: true,
	})
	production, err := NewProductionWithServices(Config{}, ProductionServices{Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = channel.Close()
		_ = production.Shutdown(context.Background())
	})
	connected, err := manager.Connect(t.Context(), session.Asset{ID: "terminal-list-legacy", Name: "terminal-list-legacy", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(t.Context(), session.OpenTabOptions{
		SessionID: connected.ID, Detached: true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	list := func() liveTabDTO {
		t.Helper()
		response := dispatchDurableTest(t, production, "terminal_list", `null`, "", "client-a")
		var rows []liveTabDTO
		requireStoreTestResponse(t, response, &rows)
		if len(rows) != 1 || rows[0].TabID != tab.ID {
			t.Fatalf("terminal_list rows = %+v, want tab %s", rows, tab.ID)
		}
		return rows[0]
	}

	initialSnapshots := factory.state.snapshotCalls.Load()
	clock.ms.Add(5000)
	beforeFirst := list()
	if beforeFirst.LastOutputAt != 0 || beforeFirst.LastOutputMSAgo != 5000 {
		t.Fatalf("before first output = %+v, want lastOutputAt=0 and legacy age=5000", beforeFirst)
	}
	if beforeFirst.LastOutputMSAgo < 1000 {
		t.Fatalf("legacy empty-output age = %d, would display as just now", beforeFirst.LastOutputMSAgo)
	}

	clock.ms.Add(10)
	if err := manager.InjectInternal(t.Context(), tab.ID, []byte("first")); err != nil {
		t.Fatal(err)
	}
	first := list()
	if first.LastOutputAt != 1_005_010 || first.LastOutputMSAgo != 0 {
		t.Fatalf("first output = %+v, want timestamp 1005010 and zero age", first)
	}
	if first.LastOutputMSAgo >= beforeFirst.LastOutputMSAgo {
		t.Fatalf("legacy unread transition did not decrease: %d -> %d", beforeFirst.LastOutputMSAgo, first.LastOutputMSAgo)
	}

	clock.ms.Add(3000)
	beforeSecond := list()
	if beforeSecond.LastOutputAt != first.LastOutputAt || beforeSecond.LastOutputMSAgo != 3000 {
		t.Fatalf("between outputs = %+v, want unchanged timestamp and age 3000", beforeSecond)
	}
	clock.ms.Add(10)
	if err := manager.InjectInternal(t.Context(), tab.ID, []byte("second")); err != nil {
		t.Fatal(err)
	}
	second := list()
	if second.LastOutputAt != 1_008_020 || second.LastOutputMSAgo != 0 {
		t.Fatalf("second output = %+v, want timestamp 1008020 and zero age", second)
	}
	if second.LastOutputMSAgo >= beforeSecond.LastOutputMSAgo {
		t.Fatalf("legacy unread transition after later output did not decrease: %d -> %d", beforeSecond.LastOutputMSAgo, second.LastOutputMSAgo)
	}
	if got := factory.state.snapshotCalls.Load(); got != initialSnapshots {
		t.Fatalf("terminal_list generated %d snapshots, want none", got-initialSnapshots)
	}
}
