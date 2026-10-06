//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type recordingDurableResolver struct {
	mu         sync.Mutex
	transports []base.Transport
	provider   base.DurableProvider
	err        error
}

func (r *recordingDurableResolver) ResolveDurable(_ context.Context, transport base.Transport) (base.DurableProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transports = append(r.transports, transport)
	return r.provider, r.err
}

func (r *recordingDurableResolver) recorded() []base.Transport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]base.Transport(nil), r.transports...)
}

type remoteDaemonFixture struct {
	client   *supervisor.Client
	provider *supervisor.RemoteProvider
}

func newRemoteDaemonFixture(t *testing.T) *remoteDaemonFixture {
	t.Helper()
	stateDir, err := os.MkdirTemp("", "nexterm-remote-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.NewServer(instance, filepath.Join(stateDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	client := supervisor.NewClient(server.SocketPath(), stateDir)
	return &remoteDaemonFixture{client: client, provider: supervisor.NewRemoteProvider(client)}
}

func newRemoteRecoveryHarness(t *testing.T, resolver session.DurableResolver) (*Production, *bridgeTestFactory) {
	t.Helper()
	factory := &bridgeTestFactory{}
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(session.Config{
		Connector:       fakeSSHConnector{},
		DurableResolver: resolver,
	})
	production, err := NewProductionWithServices(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Streams: ipc.StreamFactoryFuncs{Binary: factory.open},
	}, ProductionServices{Store: database, Sessions: manager})
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production, factory
}

func connectRemoteSSHAsset(t *testing.T, production *Production) sessionInfoDTO {
	t.Helper()
	host := "127.0.0.1"
	port := int32(22)
	username := "root"
	assetRow, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: "remote-recovery", Host: &host, Port: &port, Username: &username,
	})
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse := dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+assetRow.ID+`"}}`, "remote-ssh-connect", "client-a")
	var connected sessionInfoDTO
	requireStoreTestResponse(t, connectedResponse, &connected)
	if connected.Kind != session.KindSSH {
		t.Fatalf("connected session kind = %q, want ssh", connected.Kind)
	}
	return connected
}

func createDaemonTab(t *testing.T, provider base.DurableProvider, marker string) string {
	t.Helper()
	tabID := ids.New()
	attachment, err := provider.Create(t.Context(), base.DurableCreateOptions{
		ID: tabID, Command: []string{"sh", "-c", "printf '" + marker + "\\n'; sleep 30"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	readUntilDurableOutput(t, attachment, marker)
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	return tabID
}

func readUntilDurableOutput(t *testing.T, attachment base.DurableAttachment, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var seen []byte
	buffer := make([]byte, 4096)
	for !bytes.Contains(seen, []byte(text)) {
		if time.Now().After(deadline) {
			t.Fatalf("daemon output = %q, want %q", seen, text)
		}
		count, err := attachment.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("read daemon output: %v (seen %q)", err, seen)
		}
	}
}

func TestProductionAttachTabRecoversRemoteDaemonTabOverConnectedSSHSession(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, factory := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)
	tabID := createDaemonTab(t, fixture.provider, "remote-daemon-marker")

	channelID := "remote-recovery-channel"
	recoveredResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recoveredResponse, &recovered)
	if recovered.TabID != tabID || recovered.SessionID != connected.ID || recovered.Exited {
		t.Fatalf("recovered tab = %+v, want tab %s live on SSH session %s", recovered, tabID, connected.ID)
	}
	transport, err := production.Services.Sessions.Transport(t.Context(), connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recorded := resolver.recorded(); len(recorded) != 1 || recorded[0] != transport {
		t.Fatalf("resolver transports = %v, want only the already connected session transport", recorded)
	}
	if streams := len(factory.streams[channelID]); streams != 1 {
		t.Fatalf("bridged streams = %d, want the remote recovery", streams)
	}
	replayed := factory.at(channelID, 0)
	waitForProductionOutput(t, replayed, "remote-daemon-marker")
	if count := bytes.Count(replayed.output(), []byte("remote-daemon-marker")); count != 1 {
		t.Fatalf("remote marker replayed %d times: %q", count, replayed.output())
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
	infos, err := fixture.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == tabID {
			t.Fatal("close-tab kept the remote daemon session")
		}
	}
}

func TestProductionAttachTabRemoteEnumerationMissReturnsNotFound(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connectRemoteSSHAsset(t, production)

	missing := ids.New()
	response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+missing+`","replayBytes":1024}`, "remote-missing-channel", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("attach tab %s = %+v, want not_found", missing, response)
	}
	if len(resolver.recorded()) == 0 {
		t.Fatal("remote daemons were not enumerated")
	}
}

func TestProductionAttachTabRemoteResolverFailurePropagatesMapping(t *testing.T) {
	resolver := &recordingDurableResolver{err: durable.ErrUnavailable}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connectRemoteSSHAsset(t, production)

	missing := ids.New()
	response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+missing+`","replayBytes":1024}`, "remote-resolver-fail", "client-a")
	if response.OK || response.Error == nil {
		t.Fatalf("attach tab %s = %+v, want the resolver failure surfaced", missing, response)
	}
	if response.Error.Code == ipc.CodeNotFound {
		t.Fatalf("attach tab %s = %+v, resolver failure must not collapse into not_found", missing, response)
	}
	if response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("attach tab %s = %+v, want unsupported for an unavailable daemon", missing, response)
	}
}

type failingListDurableProvider struct {
	base.DurableProvider
	err error
}

func (p failingListDurableProvider) ListDurable(context.Context) ([]string, error) {
	return nil, p.err
}

func TestProductionAttachTabRemoteListFailurePropagatesMapping(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: failingListDurableProvider{DurableProvider: fixture.provider, err: durable.ErrUnavailable}}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connectRemoteSSHAsset(t, production)

	missing := ids.New()
	response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+missing+`","replayBytes":1024}`, "remote-list-fail", "client-a")
	if response.OK || response.Error == nil {
		t.Fatalf("attach tab %s = %+v, want the enumeration failure surfaced", missing, response)
	}
	if response.Error.Code == ipc.CodeNotFound {
		t.Fatalf("attach tab %s = %+v, enumeration failure must not collapse into not_found", missing, response)
	}
	if response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("attach tab %s = %+v, want unsupported for an unenumerable daemon", missing, response)
	}
}

func TestProductionAttachTabIgnoresUnregisteredLocalDaemon(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connectRemoteSSHAsset(t, production)

	unregistered := newRemoteDaemonFixture(t)
	tabID := createDaemonTab(t, unregistered.provider, "unregistered-daemon-marker")

	response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":1024}`, "unregistered-daemon-channel", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNotFound {
		t.Fatalf("attach tab %s = %+v, want not_found for a daemon outside the SSH resolvers", tabID, response)
	}
	if len(resolver.recorded()) == 0 {
		t.Fatal("remote daemons were not enumerated")
	}
	infos, err := unregistered.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == tabID && info.Dead {
			t.Fatal("attach attempt disturbed the unregistered daemon session")
		}
	}
}
