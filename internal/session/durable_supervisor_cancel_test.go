//go:build unix

package session

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

// stallingProxy forwards a unix socket to the supervisor daemon and can
// freeze the daemon→client direction on demand, simulating a partition
// where input frames reach the daemon but their acks never come back.
type stallingProxy struct {
	listener net.Listener
	target   string

	stall  atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func newStallingProxy(t *testing.T, target string) *stallingProxy {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-session-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	listener, err := net.Listen("unix", filepath.Join(runDir, "proxy.sock"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := &stallingProxy{listener: listener, target: target, closed: make(chan struct{})}
	t.Cleanup(func() { proxy.once.Do(func() { close(proxy.closed) }); _ = listener.Close() })
	go proxy.serve()
	return proxy
}

func (p *stallingProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.pipe(client)
	}
}

func (p *stallingProxy) pipe(client net.Conn) {
	daemon, err := net.Dial("unix", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	go func() {
		_, _ = io.Copy(daemon, client)
		_ = daemon.Close()
	}()
	go func() {
		defer func() { _ = client.Close(); _ = daemon.Close() }()
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := daemon.Read(buffer)
			if count > 0 {
				// The partition gate sits between the read and the write:
				// while frozen, frames from the daemon are dropped and the
				// client stops receiving acks deterministically.
				if !p.stall.Load() {
					if _, writeErr := client.Write(buffer[:count]); writeErr != nil {
						return
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
}

func (p *stallingProxy) freeze() { p.stall.Store(true) }
func (p *stallingProxy) thaw()   { p.stall.Store(false) }

func (p *stallingProxy) socket() string { return p.listener.Addr().String() }

// Real-supervisor A/B: with the ack path partitioned, a tab write must
// return the caller's context error instead of parking in the ack wait, the
// stream is terminated (only a reattach recovers), and the unconfirmed
// input is never replayed by the client.
func TestSupervisorDurableWriteCancelDuringAckWait(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = instance.Close()
	})
	proxy := newStallingProxy(t, server.SocketPath())
	provider := supervisor.NewRemoteProvider(supervisor.NewClient(proxy.socket(), stateDir))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tabID := ids.New()
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(ctx, Asset{ID: "cancel-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "cancel-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "stty -echo; exec cat"}},
	}); err != nil {
		t.Fatal(err)
	}

	// A: the healthy path works — input reaches the daemon and cat echoes it.
	if err := manager.Write(ctx, tabID, "client-a", []byte("before-partition-marker\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, manager, tabID, "before-partition-marker")

	// Partition the ack path, then write with a deadline: the in-progress
	// write must return the context error instead of parking in the ack wait.
	proxy.freeze()
	writeCtx, writeCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer writeCancel()
	err = manager.Write(writeCtx, tabID, "client-a", []byte("unconfirmed-input-marker\n"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partitioned Write = %v, want context deadline exceeded", err)
	}

	// The stream is terminated: a later write on the same tab fails with the
	// stream's cause instead of parking or succeeding; only a reattach
	// recovers.
	err = manager.Write(context.Background(), tabID, "client-a", []byte("after-dead-marker\n"))
	if err == nil {
		t.Fatal("Write on the terminated stream succeeded")
	}

	// The pump observes the dead stream and exits the tab (channel dropped);
	// once exited, the tab closes cleanly without touching the durable
	// record, and a reattach recovers it on a fresh stream.
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !tab.Info().Exited {
		if time.Now().After(deadline) {
			t.Fatal("tab never exited after the stream died")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := manager.closeTab(tab, false); err != nil {
		t.Fatalf("close exited tab = %v", err)
	}
	// The partition heals: the reattach flows through the proxy again.
	proxy.thaw()
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "cancel-2", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true},
	}); err != nil {
		t.Fatalf("reattach after terminated stream = %v", err)
	}
	if err := manager.Write(ctx, tabID, "client-a", []byte("after-reattach-marker\n")); err != nil {
		t.Fatalf("Write after reattach = %v", err)
	}
	output := waitForTabOutput(t, manager, tabID, "after-reattach-marker")

	// The unconfirmed input is never replayed: the client sent it at most
	// once, so cat's echo of it appears at most once in the recovered
	// output, and the post-termination marker was never delivered.
	if count := strings.Count(output, "unconfirmed-input-marker"); count > 1 {
		t.Fatalf("unconfirmed input replayed %d times: %q", count, output)
	}
	if strings.Contains(output, "after-dead-marker") {
		t.Fatalf("input written after the stream died was delivered: %q", output)
	}
	if count := strings.Count(output, "after-reattach-marker"); count != 1 {
		t.Fatalf("post-reattach input delivered %d times, want exactly 1: %q", count, output)
	}
}
