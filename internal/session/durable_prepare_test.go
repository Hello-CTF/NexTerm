package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestPrepareDurableStartsBackgroundDeploymentAndReportsReady(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		started <- struct{}{}
		select {
		case <-release:
			return provider, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		_ = manager.Close()
	})
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-prepare", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.PrepareDurable(t.Context(), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != DurablePreparing {
		t.Fatalf("first readiness = %+v; want preparing", first)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background helper deployment did not start")
	}
	again, err := manager.PrepareDurable(t.Context(), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != DurablePreparing {
		t.Fatalf("readiness while deploying = %+v; want preparing", again)
	}
	close(release)
	waitFor(t, func() bool {
		readiness, err := manager.PrepareDurable(t.Context(), connected.ID)
		return err == nil && readiness.State == DurableReady
	})
	if len(started) != 0 {
		t.Fatalf("preparing twice started %d extra deployments", len(started))
	}
	if tabs := manager.ListTabs(); len(tabs) != 0 {
		t.Fatalf("preparing created tabs: %+v", tabs)
	}
}

func TestPrepareDurableReportsUnavailableReason(t *testing.T) {
	resolver := &countingResolver{err: durable.ErrUnavailable}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-prepare-fail", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareDurable(t.Context(), connected.ID); err != nil {
		t.Fatal(err)
	}
	var readiness DurableReadiness
	waitFor(t, func() bool {
		readiness, err = manager.PrepareDurable(t.Context(), connected.ID)
		return err == nil && readiness.State == DurableUnavailable
	})
	if readiness.Reason == "" {
		t.Fatalf("unavailable readiness has no reason: %+v", readiness)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls = %d; a recent failure must not redeploy", resolver.callCount())
	}
}

func TestPrepareDurableNonSSHKinds(t *testing.T) {
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: &countingResolver{}, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	local, err := manager.Connect(t.Context(), Asset{ID: "local-prepare", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := manager.PrepareDurable(t.Context(), local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != DurableUnavailable || readiness.Reason == "" {
		t.Fatalf("local readiness = %+v; want unavailable with a reason", readiness)
	}
}

func TestPrepareDurableRegularAssetIsDisabled(t *testing.T) {
	resolver := &countingResolver{}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-regular", Kind: KindSSH, TerminalMode: TerminalModeRegular})
	if err != nil {
		t.Fatal(err)
	}
	readiness, err := manager.PrepareDurable(t.Context(), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != DurableDisabled || resolver.callCount() != 0 {
		t.Fatalf("regular asset readiness = %+v, resolver calls = %d", readiness, resolver.callCount())
	}
}
