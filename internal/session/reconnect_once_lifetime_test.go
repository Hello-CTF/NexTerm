package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
)

// The replacement channel opened by a successful ReconnectOnceFor must
// outlive the caller's context: transports bind an opened channel's
// lifetime to the context it receives, so binding the opens to the RPC
// caller's context tore the fresh channel down when the caller's context
// ended — the next write failed with "terminal tab closed". The caller's
// context still controls the attempt itself (dial and open cancellation);
// once the reconnect commits, the channel lives until the session ends.
func TestReconnectOnceForSuccessOutlivesCallerContext(t *testing.T) {
	server := newLifetimeSSHServer(t)
	config := server.clientConfig(t)
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{
		Connector: ConnectorFunc(func(ctx context.Context, asset Asset, generation uint64) (base.Transport, error) {
			return ssh.Connect(ctx, config)
		}),
		Terminals: terminals, ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()

	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(ctx, info.ID, "client-a", []byte("echo once-first\n")); err != nil {
		t.Fatal(err)
	}
	waitForLifetimeTerminalBytes(t, terminals.terminal(info.ID), "once-first", 10*time.Second)
	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}

	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	if err := manager.ReconnectOnceFor(attemptCtx, connected.ID, Asset{ID: "ssh-asset", Kind: KindSSH}); err != nil {
		t.Fatalf("reconnect once = %v", err)
	}
	// The RPC caller's context ends the moment the call returns.
	cancelAttempt()
	// The window the attempt-scoped open context used to kill the channel
	// in: wait it out, then the shell must still carry output.
	time.Sleep(500 * time.Millisecond)
	if err := manager.Write(ctx, info.ID, "client-a", []byte("echo once-second\n")); err != nil {
		t.Fatalf("write after the caller context ended: %v", err)
	}
	waitForLifetimeTerminalBytes(t, terminals.terminal(info.ID), "once-second", 10*time.Second)
}
