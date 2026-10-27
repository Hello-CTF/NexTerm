//go:build unix

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type legacyMetadataDurableProvider struct {
	base.DurableProvider
	base.DurableDescriptorLister
}

type staleDescriptorProvider struct {
	base.DurableProvider
	base.DurableDescriptorLister
	stale []base.DurableDescriptor
}

func (p *staleDescriptorProvider) ListDurableDescriptors(ctx context.Context) ([]base.DurableDescriptor, error) {
	if p.stale != nil {
		return p.stale, nil
	}
	return p.DurableDescriptorLister.ListDurableDescriptors(ctx)
}

// TestRemoteDurableTwoManagersShareSession proves the multi-client contract at
// the session layer: two independent managers (two app instances on different
// machines, each with its own SSH transport) resolve their own remote durable
// provider against the same helper daemon and share one durable session -
// discovery by listing, replay for the late joiner, live fan-out to both,
// detach without kill, size ownership after reattach, reconnect without
// replay duplication, and cross-client kill visibility.
func TestRemoteDurableTwoManagersShareSession(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-shared-")
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
	providerA := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	providerB := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	var resolvesA, resolvesB atomic.Int32
	resolverA := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		resolvesA.Add(1)
		return providerA, nil
	})
	resolverB := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		resolvesB.Add(1)
		return providerB, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	managerA := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolverA})
	t.Cleanup(func() { _ = managerA.Close() })
	managerB := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolverB})
	t.Cleanup(func() { _ = managerB.Close() })

	tabID := ids.New()
	sessionA, err := managerA.Connect(ctx, Asset{ID: "ssh-shared-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managerA.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: sessionA.ID, ClientID: "client-a", ChannelID: "shared-a1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''one\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-one")

	if err := managerA.Reconnect(ctx, sessionA.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, managerA, sessionA.ID, StatusConnected)
	if resolvesA.Load() != 2 {
		t.Fatalf("resolver calls after reconnect = %d; want 2", resolvesA.Load())
	}
	if dump := waitForTabOutput(t, managerA, tabID, "shared-one"); strings.Count(dump, "shared-one") != 1 {
		t.Fatalf("reconnect duplicated the replay: %q", dump)
	}
	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''two\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-two")

	sessionB, err := managerB.Connect(ctx, Asset{ID: "ssh-shared-b", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := managerB.ListRemoteDurable(ctx, sessionB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].ID != tabID || descriptors[0].Incarnation == "" {
		t.Fatalf("remote descriptors = %+v, want the shared session %s with an incarnation", descriptors, tabID)
	}
	if _, err := providerA.RenameDurable(ctx, base.DurableRenameOptions{
		ID: tabID, Incarnation: descriptors[0].Incarnation, RequestID: ids.New(), ExpectedMetadataRevision: descriptors[0].MetadataRevision, Title: "canonical-shared",
	}); err != nil {
		t.Fatal(err)
	}
	descriptors, err = managerB.ListRemoteDurable(ctx, sessionB.ID)
	if err != nil || len(descriptors) != 1 || descriptors[0].Title != "canonical-shared" || descriptors[0].MetadataRevision != 1 {
		t.Fatalf("canonical discovery = %+v, %v", descriptors, err)
	}
	recovered, err := managerB.RecoverRemoteDurable(ctx, descriptors[0].ID, OpenTabOptions{ClientID: "client-b", ChannelID: "shared-b1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != tabID || recovered.Title != "canonical-shared" {
		t.Fatalf("recovered tab = %+v; want canonical title", recovered)
	}
	if _, err := managerB.RenameTab(ctx, tabID, "canonical-from-b"); err != nil {
		t.Fatal(err)
	}
	descriptors, err = managerB.ListRemoteDurable(ctx, sessionB.ID)
	if err != nil || len(descriptors) != 1 || descriptors[0].Title != "canonical-from-b" || descriptors[0].MetadataRevision != 2 {
		t.Fatalf("canonical rename = %+v, %v", descriptors, err)
	}
	if _, err := providerA.RenameDurable(ctx, base.DurableRenameOptions{
		ID: tabID, Incarnation: descriptors[0].Incarnation, RequestID: ids.New(), ExpectedMetadataRevision: descriptors[0].MetadataRevision, Title: "canonical-from-a-latest",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = managerB.RenameTab(ctx, tabID, "stale-b")
	var conflict *base.DurableMetadataConflictError
	if !errors.As(err, &conflict) || conflict.CurrentTitle != "canonical-from-a-latest" || conflict.CurrentMetadataRevision != 3 {
		t.Fatalf("stale rename = %v, want current canonical conflict", err)
	}
	currentTab, err := managerB.Tab(tabID)
	if err != nil || currentTab.Info().Title != "canonical-from-a-latest" {
		t.Fatalf("conflict canonical recovery = %+v, %v", currentTab.Info(), err)
	}
	if resolvesB.Load() != 1 {
		t.Fatalf("second manager resolver calls = %d; want 1", resolvesB.Load())
	}
	if dump := waitForTabOutput(t, managerB, tabID, "shared-two"); strings.Count(dump, "shared-one") != 1 || strings.Count(dump, "shared-two") != 1 {
		t.Fatalf("late joiner replay = %q", dump)
	}

	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''three\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-three")
	waitForTabOutput(t, managerB, tabID, "shared-three")

	if err := managerA.DetachAll(tabID); err != nil {
		t.Fatal(err)
	}
	listed, err := providerB.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != tabID {
		t.Fatalf("detach killed the shared session: %v", listed)
	}
	if err := managerB.Write(ctx, tabID, "client-b", []byte("echo shared-''four\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerB, tabID, "shared-four")
	tabA, err := managerA.Tab(tabID)
	if err != nil {
		t.Fatalf("detached tab vanished: %v", err)
	}
	if tabA.Info().Exited {
		t.Fatal("detached tab reported exited")
	}

	if _, err := managerA.AttachTab(ctx, tabID, AttachOptions{ClientID: "client-a", ChannelID: "shared-a2"}); err != nil {
		t.Fatal(err)
	}
	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''five\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-five")
	waitForTabOutput(t, managerB, tabID, "shared-five")

	if err := managerB.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	listed, err = providerA.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("close on one manager did not kill the shared session: %v", listed)
	}
	waitForTabExit(t, managerA, tabID)
}

func TestRecoverRemoteDurableRejectsChangedIncarnation(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-identity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	providerA := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	providerB := &staleDescriptorProvider{
		DurableProvider: providerA, DurableDescriptorLister: providerA,
	}
	resolver := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		return providerB, nil
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-identity", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabID := ids.New()
	attachment, err := providerA.Create(ctx, base.DurableCreateOptions{ID: tabID, Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	_ = attachment.Close()
	descriptors, err := manager.ListRemoteDurable(ctx, connected.ID)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("discovery = %+v, %v", descriptors, err)
	}
	identity := base.DurableIdentity{CreatedAt: descriptors[0].CreatedAt, Incarnation: descriptors[0].Incarnation}
	providerB.stale = descriptors
	if err := providerA.KillWithIdentity(ctx, tabID, identity); err != nil {
		t.Fatal(err)
	}
	replacement, err := providerA.Create(ctx, base.DurableCreateOptions{ID: tabID, Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	if _, err := manager.RecoverRemoteDurable(ctx, tabID, OpenTabOptions{ClientID: "client-b", ChannelID: "identity-channel", Cols: 80, Rows: 24}); !errors.Is(err, durable.ErrIdentity) {
		t.Fatalf("recovery with stale incarnation = %v, want identity changed", err)
	}
}

func TestRenameAfterConflictUsesNewAttempt(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-conflict-retry-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		return provider, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-conflict-retry", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabID := ids.New()
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "conflict-retry-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "sleep 30"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RenameTab(ctx, tabID, "canonical-title"); err != nil {
		t.Fatal(err)
	}
	infos, err := instance.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("initial infos = %+v, %v", infos, err)
	}
	if _, err := provider.RenameDurable(ctx, base.DurableRenameOptions{
		ID: tabID, Incarnation: infos[0].Incarnation, RequestID: ids.New(), ExpectedMetadataRevision: 1, Title: "canonical-title",
	}); err != nil {
		t.Fatal(err)
	}
	var conflict *base.DurableMetadataConflictError
	if _, err := manager.RenameTab(ctx, tabID, "canonical-title"); !errors.As(err, &conflict) {
		t.Fatalf("first conflicting rename = %v, want conflict", err)
	}
	if _, err := manager.RenameTab(ctx, tabID, "canonical-title"); err != nil {
		t.Fatalf("same-title retry = %v, want success with a new attempt", err)
	}
	infos, err = instance.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Title != "canonical-title" || infos[0].MetadataRevision != 3 {
		t.Fatalf("same-title retry metadata = %+v, %v", infos, err)
	}
}

func TestRenameDurableRecoversReplacementIncarnation(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-rename-identity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		return provider, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-rename-identity", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabID := ids.New()
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "rename-identity-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "sleep 30"}},
	}); err != nil {
		t.Fatal(err)
	}
	infos, err := instance.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("initial infos = %+v, %v", infos, err)
	}
	if err := provider.KillWithIdentity(ctx, tabID, base.DurableIdentity{CreatedAt: infos[0].CreatedAt, Incarnation: infos[0].Incarnation}); err != nil {
		t.Fatal(err)
	}
	replacement, err := provider.Create(ctx, base.DurableCreateOptions{ID: tabID, Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	_ = replacement.Close()
	infos, err = instance.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("replacement infos = %+v, %v", infos, err)
	}
	if _, err := provider.RenameDurable(ctx, base.DurableRenameOptions{
		ID: tabID, Incarnation: infos[0].Incarnation, RequestID: ids.New(), Title: "stale-local",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RenameTab(ctx, tabID, "stale-local"); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(tabID)
	if err != nil || tab.Info().Title != "stale-local" {
		t.Fatalf("identity canonical recovery = %+v, %v", tab.Info(), err)
	}
	if _, err := manager.RenameTab(ctx, tabID, "stale-local"); err != nil {
		t.Fatal(err)
	}
	infos, err = instance.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Title != "stale-local" || infos[0].MetadataRevision != 2 {
		t.Fatalf("rename after identity recovery = %+v, %v", infos, err)
	}
}

func TestRecoverRemoteDurableFallsBackToLocalTitleOnlyWithoutMetadata(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-legacy-title-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	ctx := context.Background()
	provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	tabID := ids.New()
	attachment, err := provider.Create(ctx, base.DurableCreateOptions{ID: tabID, Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	_ = attachment.Close()
	infos, err := instance.List(ctx)
	if err != nil || len(infos) != 1 {
		t.Fatalf("initial infos = %+v, %v", infos, err)
	}
	if _, err := provider.RenameDurable(ctx, base.DurableRenameOptions{
		ID: tabID, Incarnation: infos[0].Incarnation, RequestID: ids.New(), Title: "remote-canonical",
	}); err != nil {
		t.Fatal(err)
	}
	legacy := &legacyMetadataDurableProvider{DurableProvider: provider, DurableDescriptorLister: provider}
	localTitles := newFakeDurableProvider()
	localTitles.PersistDurableTabTitleContext(context.Background(), tabID, "local-fallback")
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), TabTitles: localTitles,
		DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
			return legacy, nil
		}),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "ssh-legacy-title", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.RecoverRemoteDurable(context.Background(), tabID, OpenTabOptions{SessionID: connected.ID, ClientID: "client-legacy", ChannelID: "legacy-title-channel", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Title != "local-fallback" {
		t.Fatalf("legacy recovery title = %q, want local fallback", recovered.Title)
	}
}

func TestRenameConcurrentReconnectRace(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-rename-reconnect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		return provider, nil
	})})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-rename-reconnect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tabID := ids.New()
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "rename-reconnect-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "sleep 30"}},
	}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for index := 0; index < 50; index++ {
			if _, err := manager.RenameTab(ctx, tabID, "rename-reconnect-race"); err != nil && !errors.Is(err, ErrDisconnected) {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for index := 0; index < 25; index++ {
			if err := manager.Reconnect(ctx, connected.ID); err != nil {
				errs <- err
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCloseTabSerializeFallbackRename(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	for index := 0; index < 50; index++ {
		info, err := manager.OpenTab(ctx, OpenTabOptions{
			SessionID: connected.ID, ClientID: "client-a", ChannelID: "fallback-close-race", Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = manager.RenameTab(ctx, info.ID, "fallback-title")
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = manager.CloseTab(info.ID)
		}()
		close(start)
		wg.Wait()
		if got := provider.DurableTabTitleContext(context.Background(), info.ID); got != "" {
			t.Fatalf("fallback title survived close: %q", got)
		}
	}
}

func TestCloseTabSerializeOrdinaryRename(t *testing.T) {
	manager, _, _, connected := newDurableTestManager(t, newFakeDurableProvider())
	ctx := context.Background()
	for index := 0; index < 50; index++ {
		info, err := manager.OpenTab(ctx, OpenTabOptions{
			SessionID: connected.ID, ClientID: "client-a", ChannelID: "ordinary-close-race", Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = manager.RenameTab(ctx, info.ID, "ordinary-title")
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = manager.CloseTab(info.ID)
		}()
		close(start)
		wg.Wait()
		if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
			t.Fatalf("ordinary tab survived close: %v", err)
		}
	}
}

func waitForSessionStatus(t *testing.T, manager *Manager, id string, status Status) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, info := range manager.ListSessions() {
			if info.ID == id && info.Status == status {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never reached status %s", id, status)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-manager.ctx.Done():
			t.Fatal("manager closed while waiting for the session status")
		}
	}
}

func TestListRemoteDurableRejectsUnavailableSessionScopes(t *testing.T) {
	resolver := &countingResolver{provider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	local, err := manager.Connect(ctx, Asset{ID: "scope-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ListRemoteDurable(ctx, local.ID); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local ListRemoteDurable error = %v, want ErrUnsupported", err)
	}
	device, err := manager.Connect(ctx, Asset{ID: "device/scope-device", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ListRemoteDurable(ctx, device.ID); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnected device ListRemoteDurable error = %v, want ErrDisconnected", err)
	}
	ssh, err := manager.Connect(ctx, Asset{ID: "scope-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect(ssh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ListRemoteDurable(ctx, ssh.ID); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("disconnected ListRemoteDurable error = %v, want ErrDisconnected", err)
	}
	if _, err := manager.ListRemoteDurable(ctx, "scope-missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing ListRemoteDurable error = %v, want ErrSessionNotFound", err)
	}
	if calls := resolver.callCount(); calls != 0 {
		t.Fatalf("resolver calls for rejected scopes = %d, want 0", calls)
	}
}

func TestRecoverRemoteDurableHonorsSessionScope(t *testing.T) {
	providerA := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	providerB := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	var transportA, transportB base.Transport
	resolver := DurableResolverFunc(func(_ context.Context, transport base.Transport) (base.DurableProvider, error) {
		switch transport {
		case transportA:
			return providerA, nil
		case transportB:
			return providerB, nil
		default:
			return nil, errors.New("unexpected transport")
		}
	})
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	sessionA, err := manager.Connect(ctx, Asset{ID: "scope-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	transportA, err = manager.Transport(ctx, sessionA.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := manager.Connect(ctx, Asset{ID: "scope-b", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	transportB, err = manager.Transport(ctx, sessionB.ID)
	if err != nil {
		t.Fatal(err)
	}

	requested := sessionB
	requestedProvider, otherProvider := providerB, providerA
	if sessionA.ID > sessionB.ID {
		requested = sessionA
		requestedProvider, otherProvider = providerA, providerB
	}
	seed := func(provider *listingDurableProvider, marker string) {
		attachment, err := provider.Create(ctx, base.DurableCreateOptions{ID: "scope-shared-id", Cols: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		if err := attachment.(*fakeDurableAttachment).emit([]byte(marker)); err != nil {
			t.Fatal(err)
		}
		if err := attachment.Close(); err != nil {
			t.Fatal(err)
		}
	}
	seed(requestedProvider, "scope-requested")
	seed(otherProvider, "scope-other")
	recovered, err := manager.RecoverRemoteDurable(ctx, "scope-shared-id", OpenTabOptions{
		SessionID: requested.ID, ClientID: "scope-client", ChannelID: "scope-channel", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.SessionID != requested.ID {
		t.Fatalf("recovered session = %s, want scoped session %s", recovered.SessionID, requested.ID)
	}
	if output := waitForTabOutput(t, manager, recovered.ID, "scope-requested"); strings.Contains(output, "scope-other") {
		t.Fatalf("scoped recovery read the other provider: %q", output)
	}
	if err := manager.CloseTab(recovered.ID); err != nil {
		t.Fatal(err)
	}
}

func waitForTabExit(t *testing.T, manager *Manager, tabID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		tab, err := manager.Tab(tabID)
		if err != nil {
			t.Fatalf("tab %s vanished before its exit was observed: %v", tabID, err)
		}
		if tab.Info().Exited {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("tab never observed the peer kill")
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-manager.ctx.Done():
			t.Fatal("manager closed while waiting for the tab exit")
		}
	}
}
