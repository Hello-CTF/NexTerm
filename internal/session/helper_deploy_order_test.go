package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func init() {
	// Most tests exercise the deployment itself rather than its pacing, so they
	// start it as soon as the ordinary shell exists.
	helperDeploymentGrace = time.Millisecond
	helperDeploymentSettle = time.Millisecond
}

// waitHelperSettled blocks until the deferred helper deployment of the session
// has finished and recorded its outcome.
func waitHelperSettled(t testing.TB, manager *Manager, sessionID string) {
	t.Helper()
	session, err := manager.Session(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		backgroundHelperDeployments.Lock()
		running := backgroundHelperDeployments.entries[session] != nil
		backgroundHelperDeployments.Unlock()
		session.durableMu.Lock()
		settled := session.durableProvider != nil || session.durableFailure != nil
		session.durableMu.Unlock()
		if !running && settled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("helper deployment did not settle")
}

func setHelperDeploymentPacing(t *testing.T, grace, settle time.Duration) {
	t.Helper()
	oldGrace, oldSettle := helperDeploymentGrace, helperDeploymentSettle
	helperDeploymentGrace, helperDeploymentSettle = grace, settle
	t.Cleanup(func() { helperDeploymentGrace, helperDeploymentSettle = oldGrace, oldSettle })
}

type deploymentProbe struct {
	started   chan struct{}
	release   chan struct{}
	ptyAtHelp chan int
}

func newDeploymentProbe(transportChannels func() int) (*deploymentProbe, DurableResolverFunc) {
	probe := &deploymentProbe{started: make(chan struct{}), release: make(chan struct{}), ptyAtHelp: make(chan int, 1)}
	return probe, func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		probe.ptyAtHelp <- transportChannels()
		close(probe.started)
		select {
		case <-probe.release:
			return newFakeDurableProvider(), nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

func TestFirstTabOpensShellBeforeHelperDeploymentTouchesTheConnection(t *testing.T) {
	setHelperDeploymentPacing(t, time.Hour, 5*time.Millisecond)
	connector := newFakeConnector()
	probe, resolver := newDeploymentProbe(func() int { return connector.transport(0).channelCount() })
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-first-connect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "first", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil || info.Durable {
		t.Fatalf("first tab = %+v, %v; want an ordinary shell", info, err)
	}
	select {
	case <-probe.started:
		t.Fatal("helper deployment started before the shell printed anything")
	default:
	}
	if err := connector.transport(0).channel(0).emit([]byte("Last login: today\r\n$ ")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.started:
	case <-time.After(5 * time.Second):
		t.Fatal("helper deployment did not start after the shell's first output")
	}
	if got := <-probe.ptyAtHelp; got != 1 {
		t.Fatalf("PTY channels when the deployment began = %d; want the shell opened first", got)
	}
	// A slow deployment must not stop the shell from receiving output.
	if err := connector.transport(0).channel(0).emit([]byte("ls\r\n")); err != nil {
		t.Fatal(err)
	}
	close(probe.release)
	waitHelperSettled(t, manager, connected.ID)
}

func TestSilentShellStillGetsHelperDeploymentAfterGrace(t *testing.T) {
	setHelperDeploymentPacing(t, 10*time.Millisecond, time.Hour)
	connector := newFakeConnector()
	probe, resolver := newDeploymentProbe(func() int { return connector.transport(0).channelCount() })
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-silent-shell", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "silent", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe.started:
	case <-time.After(5 * time.Second):
		t.Fatal("helper deployment never started for a silent shell")
	}
	close(probe.release)
	waitHelperSettled(t, manager, connected.ID)
}

func TestClosingTheTabBeforeFirstOutputCancelsDeferredDeployment(t *testing.T) {
	setHelperDeploymentPacing(t, 100*time.Millisecond, time.Millisecond)
	connector := newFakeConnector()
	var calls atomic.Int32
	resolver := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		calls.Add(1)
		return newFakeDurableProvider(), nil
	})
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-closed-early", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client", ChannelID: "early", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{}, DurableFallback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond) // well past the grace period
	if calls.Load() != 0 {
		t.Fatalf("resolver calls = %d; a closed tab must not trigger deployment", calls.Load())
	}
}
