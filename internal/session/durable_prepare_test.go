package session

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
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

func TestPrepareDurableStartsNewDeploymentAfterRetryWindow(t *testing.T) {
	var offset atomic.Int64
	resolver := &countingResolver{err: durable.ErrUnavailable}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true,
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).Add(time.Duration(offset.Load())) },
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-prepare-window", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareDurable(t.Context(), connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		readiness, err := manager.PrepareDurable(t.Context(), connected.ID)
		return err == nil && readiness.State == DurableUnavailable
	})
	offset.Store(int64(durableFailureRetryWindow - time.Second))
	if readiness, err := manager.PrepareDurable(t.Context(), connected.ID); err != nil || readiness.State != DurableUnavailable {
		t.Fatalf("readiness inside the retry window = %+v, %v", readiness, err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("resolver calls inside the retry window = %d; want 1", resolver.callCount())
	}
	offset.Store(int64(durableFailureRetryWindow + time.Second))
	readiness, err := manager.PrepareDurable(t.Context(), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != DurablePreparing {
		t.Fatalf("readiness after the retry window = %+v; want a fresh deployment", readiness)
	}
	waitFor(t, func() bool { return resolver.callCount() == 2 })
}

func TestPrepareDurableRejectsAnotherUsersSession(t *testing.T) {
	resolver := &countingResolver{}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "ssh-owned", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareDurable(requestTestUser("owner-b"), connected.ID); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("other user prepare = %v; want forbidden", err)
	}
	if resolver.callCount() != 0 {
		t.Fatalf("other user's prepare started %d deployments", resolver.callCount())
	}
	if readiness, err := manager.PrepareDurable(requestTestUser("owner-a"), connected.ID); err != nil || readiness.State != DurablePreparing {
		t.Fatalf("owner prepare = %+v, %v", readiness, err)
	}
}

func TestPrepareDurableRacingCompletionAndTabsDeploysOnce(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	release := make(chan struct{})
	var calls atomic.Int32
	resolver := DurableResolverFunc(func(ctx context.Context, _ base.Transport) (base.DurableProvider, error) {
		calls.Add(1)
		select {
		case <-release:
			return provider, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver, OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-prepare-race", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareDurable(t.Context(), connected.ID); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := 0; ; index++ {
				select {
				case <-stop:
					return
				default:
				}
				if worker == 0 && index < 4 {
					if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
						SessionID: connected.ID, ClientID: "client", ChannelID: "race-" + strconv.Itoa(index), Cols: 80, Rows: 24,
						Durable: &DurableTabOptions{}, DurableFallback: true,
					}); err != nil {
						errs <- err
						return
					}
					continue
				}
				if _, err := manager.PrepareDurable(context.Background(), connected.ID); err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	time.Sleep(5 * time.Millisecond)
	close(release)
	waitFor(t, func() bool {
		readiness, err := manager.PrepareDurable(t.Context(), connected.ID)
		return err == nil && readiness.State == DurableReady
	})
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("helper deployments = %d; want 1", got)
	}
}
