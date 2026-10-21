package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestConnectRedialsDeadTransportWithFreshAsset(t *testing.T) {
	for _, kind := range []string{KindSSH, KindLocal} {
		t.Run(kind, func(t *testing.T) {
			connector := newFakeConnector()
			var dialed []Asset
			connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
				dialed = append(dialed, asset)
				return newFakeTransport(asset.Kind, uint64(call)+1000), nil
			}
			manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
			t.Cleanup(func() { _ = manager.Close() })
			ctx := requestTestUser("owner-a")
			initial := Asset{ID: "dead-asset", Name: "old", Kind: kind, Options: map[string]any{"strictHostKey": false}, ConfigFingerprint: "old-v1"}
			refreshed := Asset{ID: "dead-asset", Name: "strict", Kind: kind, Options: map[string]any{"strictHostKey": true}, ConfigFingerprint: "strict-v2"}
			if _, err := manager.Connect(ctx, initial); err != nil {
				t.Fatal(err)
			}
			connector.transport(0).alive.Store(false)

			connected, err := manager.Connect(ctx, refreshed)
			if err != nil {
				t.Fatal(err)
			}
			if got := connector.callCount(); got != 2 {
				t.Fatalf("connector calls = %d, want a fresh dial after the dead transport", got)
			}
			options, ok := dialed[1].Options.(map[string]any)
			if !ok || options["strictHostKey"] != true || dialed[1].ConfigFingerprint != "strict-v2" {
				t.Fatalf("redial asset = %+v, want refreshed strict asset", dialed[1])
			}
			transport, err := manager.Transport(ctx, connected.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !transport.IsAlive() {
				t.Fatal("Connect returned a session with a dead transport")
			}
			result, err := transport.Exec(ctx, "echo healthy", base.ExecOptions{ExpectedGeneration: transport.Generation()})
			if err != nil || result.Stdout != "ran:echo healthy" {
				t.Fatalf("Exec on reconnected transport = %+v, %v", result, err)
			}
			channel, err := transport.(base.PTYTransport).OpenPTY(ctx, base.PTYOptions{Cols: 80, Rows: 24, ExpectedGeneration: transport.Generation()})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = channel.Close() }()
			if written, err := channel.Write([]byte("healthy")); err != nil || written != len("healthy") {
				t.Fatalf("PTY Write on reconnected transport = %d, %v", written, err)
			}
		})
	}
}

func TestConnectDuringReconnectReturnsHealthySession(t *testing.T) {
	connector := newFakeConnector()
	reconnectStarted := make(chan struct{})
	connector.connect = func(ctx context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		if call == 2 {
			close(reconnectStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return newFakeTransport(asset.Kind, uint64(call)+1000), nil
	}
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory()})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := requestTestUser("owner-a")
	asset := Asset{ID: "reconnecting-asset", Kind: KindSSH}
	connected, err := manager.Connect(ctx, asset)
	if err != nil {
		t.Fatal(err)
	}
	reconnectResult := make(chan error, 1)
	go func() { reconnectResult <- manager.Reconnect(context.Background(), connected.ID) }()
	select {
	case <-reconnectStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect did not reach the blocked dial")
	}

	type connectResult struct {
		session *Session
		err     error
	}
	replaced := make(chan connectResult, 1)
	go func() {
		session, err := manager.Connect(ctx, Asset{ID: asset.ID, Kind: KindSSH, ConfigFingerprint: "strict-v2"})
		replaced <- connectResult{session: session, err: err}
	}()
	var result connectResult
	select {
	case result = <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not replace the in-flight reconnect")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := <-reconnectResult; err == nil {
		t.Fatal("stale reconnect succeeded after the healthy replacement")
	}
	if got := connector.callCount(); got != 3 {
		t.Fatalf("connector calls = %d, want initial, stale reconnect, and replacement dials", got)
	}
	transport, err := manager.Transport(ctx, result.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !transport.IsAlive() {
		t.Fatal("Connect returned while the replacement transport was unhealthy")
	}
}
