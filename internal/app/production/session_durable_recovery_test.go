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

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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

func createDaemonTabWithID(t *testing.T, provider base.DurableProvider, tabID, marker string) {
	t.Helper()
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
}

type transportDurableResolver struct {
	mu        sync.Mutex
	providers map[base.Transport]base.DurableProvider
}

func (r *transportDurableResolver) ResolveDurable(_ context.Context, transport base.Transport) (base.DurableProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[transport]
	if provider == nil {
		return nil, durable.ErrUnavailable
	}
	return provider, nil
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

func TestProductionTerminalAttachDurableFailureDoesNotFallBackToPTY(t *testing.T) {
	resolver := &recordingDurableResolver{err: durable.ErrUnavailable}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	plainResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "plain-ssh-channel", "client-a")
	plainTabID := requireAttachedTabID(t, plainResponse)
	plainTab, err := production.Services.Sessions.Tab(plainTabID)
	if err != nil {
		t.Fatal(err)
	}
	if plainTab.Info().Durable {
		t.Fatal("ordinary SSH attach became durable")
	}

	durableResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "durable-ssh-channel", "client-a")
	if durableResponse.OK || durableResponse.Error == nil || durableResponse.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("explicit durable attach with unavailable helper = %+v, want unsupported without a PTY fallback", durableResponse)
	}
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 1 || tabs[0].ID != plainTabID || tabs[0].Durable {
		t.Fatalf("tabs after failed durable attach = %+v, want only the ordinary tab", tabs)
	}
}

func TestProductionListDurableUsesOnlyRequestedSSHSession(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	first := connectRemoteSSHAsset(t, production)
	second := connectRemoteSSHAsset(t, production)

	response := dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+second.ID+`","durable":true}`, "", "client-a")
	var descriptors []struct {
		ID          string `json:"id"`
		Incarnation string `json:"incarnation"`
		CreatedAt   int64  `json:"createdAt"`
		Cols        uint32 `json:"cols"`
		Rows        uint32 `json:"rows"`
		Dead        bool   `json:"dead"`
		ExitCode    *int   `json:"exitCode"`
		FinishedAt  *int64 `json:"finishedAt"`
	}
	requireStoreTestResponse(t, response, &descriptors)
	if string(response.Data) != "[]" {
		t.Fatalf("empty descriptor response = %s, want []", response.Data)
	}
	if len(descriptors) != 0 {
		t.Fatalf("empty remote descriptor list = %+v, want no entries", descriptors)
	}
	transport, err := production.Services.Sessions.Transport(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorded := resolver.recorded()
	if len(recorded) != 1 || recorded[0] != transport {
		t.Fatalf("descriptor resolver calls = %v, want only the requested second SSH transport", recorded)
	}
	if first.ID == second.ID {
		t.Fatal("test requires two distinct SSH sessions")
	}
}

func TestProductionListDurableResolverFailureIsNotAnEmptyList(t *testing.T) {
	resolver := &recordingDurableResolver{err: durable.ErrUnavailable}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	response := dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+connected.ID+`","durable":true}`, "", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("descriptor resolver failure = %+v, want unsupported instead of an empty list", response)
	}
}

type failingDescriptorListProvider struct {
	base.DurableProvider
	err error
}

func (p failingDescriptorListProvider) ListDurableDescriptors(context.Context) ([]base.DurableDescriptor, error) {
	return nil, p.err
}

func TestProductionListDurableEnumerationFailureIsNotAnEmptyList(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: failingDescriptorListProvider{DurableProvider: fixture.provider, err: durable.ErrUnavailable}}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	response := dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+connected.ID+`","durable":true}`, "", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("descriptor enumeration failure = %+v, want unsupported instead of an empty list", response)
	}
}

func TestProductionTwoManagersDiscoverAndShareExplicitDurableSSH(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolverA := &recordingDurableResolver{provider: fixture.provider}
	resolverC := &recordingDurableResolver{provider: fixture.provider}
	productionA, factoryA := newRemoteRecoveryHarness(t, resolverA)
	productionC, factoryC := newRemoteRecoveryHarness(t, resolverC)
	connectedA := connectRemoteSSHAsset(t, productionA)
	connectedC := connectRemoteSSHAsset(t, productionC)
	if connectedA.AssetID == nil || connectedC.AssetID == nil || *connectedA.AssetID == *connectedC.AssetID {
		t.Fatalf("independent clients need different asset IDs: A=%+v C=%+v", connectedA.AssetID, connectedC.AssetID)
	}

	channelA := "production-shared-a-1"
	createResponse := dispatchDurableTest(t, productionA, "terminal_attach", `{"sessionId":"`+connectedA.ID+`","cols":80,"rows":24,"durable":true}`, channelA, "client-a")
	tabID := requireAttachedTabID(t, createResponse)
	tabA, err := productionA.Services.Sessions.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if !tabA.Info().Durable {
		t.Fatal("explicit SSH durable attach produced a volatile tab")
	}
	streamA := factoryA.at(channelA, 0)
	writeDurableTestInput(t, productionA, tabID, "printf 'production-a-before\\n'", "client-a")
	waitForProductionOutput(t, streamA, "production-a-before")

	if tabs := productionC.Services.Sessions.ListTabs(); len(tabs) != 0 {
		t.Fatalf("client C already knows tabs before discovery: %+v", tabs)
	}
	infos, err := fixture.client.List(context.Background())
	if err != nil || len(infos) != 1 {
		t.Fatalf("fixture infos = %+v, %v", infos, err)
	}
	if _, err := fixture.provider.RenameDurable(context.Background(), base.DurableRenameOptions{
		ID: tabID, Incarnation: infos[0].Incarnation, RequestID: ids.New(), Title: "production-canonical",
	}); err != nil {
		t.Fatal(err)
	}
	listResponse := dispatchDurableTest(t, productionC, "terminal_list", `{"sessionId":"`+connectedC.ID+`","durable":true}`, "", "client-c")
	var descriptors []struct {
		ID               string `json:"id"`
		Incarnation      string `json:"incarnation"`
		CreatedAt        int64  `json:"createdAt"`
		Title            string `json:"title"`
		MetadataRevision uint64 `json:"metadataRevision"`
		Cols             uint32 `json:"cols"`
		Rows             uint32 `json:"rows"`
		Dead             bool   `json:"dead"`
		ExitCode         *int   `json:"exitCode"`
		FinishedAt       *int64 `json:"finishedAt"`
	}
	requireStoreTestResponse(t, listResponse, &descriptors)
	if len(descriptors) != 1 || descriptors[0].ID != tabID || descriptors[0].Incarnation == "" ||
		descriptors[0].CreatedAt <= 0 || descriptors[0].Cols != 80 || descriptors[0].Rows != 24 || descriptors[0].Dead ||
		descriptors[0].ExitCode != nil || descriptors[0].FinishedAt != nil ||
		descriptors[0].Title != "production-canonical" || descriptors[0].MetadataRevision != 1 {
		t.Fatalf("client C descriptors = %+v, want only A's live durable tab %s", descriptors, tabID)
	}
	incarnation := descriptors[0].Incarnation

	channelC := "production-shared-c-1"
	attachResponse := dispatchDurableTest(t, productionC, "terminal_attach_tab", `{"tabId":"`+descriptors[0].ID+`","sessionId":"`+connectedC.ID+`","replayBytes":262144}`, channelC, "client-c")
	var attachedC attachedTabDTO
	requireStoreTestResponse(t, attachResponse, &attachedC)
	if attachedC.TabID != tabID || attachedC.SessionID != connectedC.ID || attachedC.Title != "production-canonical" {
		t.Fatalf("client C attached = %+v, want canonical title on its own SSH session", attachedC)
	}
	streamC := factoryC.at(channelC, 0)
	waitForProductionOutput(t, streamC, "production-a-before")
	writeDurableTestInput(t, productionC, tabID, "printf 'production-c-shared\\n'", "client-c")
	waitForProductionOutput(t, streamA, "production-c-shared")
	waitForProductionOutput(t, streamC, "production-c-shared")

	beforeDuplicate, err := fixture.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	duplicateResponse := dispatchDurableTest(t, productionC, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":1024}`, "production-shared-c-2", "client-c")
	requireStoreTestResponse(t, duplicateResponse, &attachedC)
	afterDuplicate, err := fixture.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeDuplicate) != 1 || len(afterDuplicate) != 1 || afterDuplicate[0].ID != tabID || afterDuplicate[0].Incarnation != incarnation {
		t.Fatalf("duplicate attach changed the remote shell: before=%+v after=%+v", beforeDuplicate, afterDuplicate)
	}

	requireProductionNull(t, dispatchDurableTest(t, productionA, "terminal_detach", `{"tabId":"`+tabID+`","channelId":"`+channelA+`"}`, "", "client-a"))
	writeDurableTestInput(t, productionC, tabID, "printf 'production-c-after-detach\\n'", "client-c")
	waitForProductionOutput(t, streamC, "production-c-after-detach")
	afterDetach, err := fixture.client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDetach) != 1 || afterDetach[0].ID != tabID || afterDetach[0].Incarnation != incarnation || afterDetach[0].Dead {
		t.Fatalf("A detach changed the remote process: %+v", afterDetach)
	}

	channelA2 := "production-shared-a-2"
	reattachResponse := dispatchDurableTest(t, productionA, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, channelA2, "client-a")
	var attachedA attachedTabDTO
	requireStoreTestResponse(t, reattachResponse, &attachedA)
	streamA2 := factoryA.at(channelA2, 0)
	writeDurableTestInput(t, productionA, tabID, "printf 'production-a-after-reattach\\n'", "client-a")
	waitForProductionOutput(t, streamA2, "production-a-after-reattach")
	waitForProductionOutput(t, streamC, "production-a-after-reattach")

	requireProductionNull(t, dispatchDurableTest(t, productionC, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-c"}`, "", "client-c"))
}

func TestProductionAttachTabHonorsDiscoveredSessionScope(t *testing.T) {
	firstDaemon := newRemoteDaemonFixture(t)
	secondDaemon := newRemoteDaemonFixture(t)
	resolver := &transportDurableResolver{providers: make(map[base.Transport]base.DurableProvider)}
	production, factory := newRemoteRecoveryHarness(t, resolver)
	first := connectRemoteSSHAsset(t, production)
	second := connectRemoteSSHAsset(t, production)

	requested, other := second, first
	requestedDaemon, otherDaemon := secondDaemon, firstDaemon
	if first.ID > second.ID {
		requested, other = first, second
		requestedDaemon, otherDaemon = firstDaemon, secondDaemon
	}
	if requested.ID == other.ID {
		t.Fatal("test requires two distinct SSH sessions")
	}
	requestedTransport, err := production.Services.Sessions.Transport(t.Context(), requested.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherTransport, err := production.Services.Sessions.Transport(t.Context(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolver.providers[requestedTransport] = requestedDaemon.provider
	resolver.providers[otherTransport] = otherDaemon.provider

	tabID := ids.New()
	createDaemonTabWithID(t, requestedDaemon.provider, tabID, "scope-requested")
	createDaemonTabWithID(t, otherDaemon.provider, tabID, "scope-other")

	listResponse := dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+requested.ID+`","durable":true}`, "", "client-a")
	var descriptors []struct {
		ID string `json:"id"`
	}
	requireStoreTestResponse(t, listResponse, &descriptors)
	if len(descriptors) != 1 || descriptors[0].ID != tabID {
		t.Fatalf("requested host descriptors = %+v, want shared id %s", descriptors, tabID)
	}

	channelID := "scoped-durable-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+descriptors[0].ID+`","sessionId":"`+requested.ID+`","replayBytes":262144}`, channelID, "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, attachResponse, &attached)
	if attached.SessionID != requested.ID {
		t.Fatalf("scoped attach used session %s, want discovery session %s", attached.SessionID, requested.ID)
	}
	stream := factory.at(channelID, 0)
	waitForProductionOutput(t, stream, "scope-requested")
	if bytes.Contains(stream.output(), []byte("scope-other")) {
		t.Fatalf("scoped attach replayed the other daemon: %q", stream.output())
	}
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a"}`, "", "client-a"))
}

type flakyMetadataProvider struct {
	base.DurableProvider
	base.DurableMetadataProvider
	base.DurableDescriptorLister
	base.DurableIdentityAttacher
	calls     []base.DurableRenameOptions
	failFirst error
}

func (p *flakyMetadataProvider) DurableMetadataSupported(context.Context) (bool, error) {
	return true, nil
}

func (p *flakyMetadataProvider) RenameDurable(ctx context.Context, options base.DurableRenameOptions) (base.DurableRenameResult, error) {
	result, err := p.DurableMetadataProvider.RenameDurable(ctx, options)
	p.calls = append(p.calls, options)
	if len(p.calls) == 1 && p.failFirst != nil && err == nil {
		return base.DurableRenameResult{}, p.failFirst
	}
	return result, err
}

func TestProductionRenameRetriesPreserveRequestAndRevision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failFirst error
		wantCalls int
	}{
		{name: "helper committed before production response loss", failFirst: base.ErrDisconnected, wantCalls: 2},
		{name: "production committed before app response loss", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRemoteDaemonFixture(t)
			provider := &flakyMetadataProvider{
				DurableProvider: fixture.provider, DurableMetadataProvider: fixture.provider,
				DurableDescriptorLister: fixture.provider, DurableIdentityAttacher: fixture.provider, failFirst: tc.failFirst,
			}
			resolver := &recordingDurableResolver{provider: provider}
			production, _ := newRemoteRecoveryHarness(t, resolver)
			connected := connectRemoteSSHAsset(t, production)
			createResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "rename-retry-channel", "client-a")
			tabID := requireAttachedTabID(t, createResponse)
			rename := func() ipc.Response {
				return dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"`+tabID+`","title":"canonical-retry"}`, "", "client-a")
			}
			first := rename()
			if tc.failFirst != nil && first.OK {
				t.Fatalf("first unknown-result response = %+v, want failure", first)
			}
			second := rename()
			if !second.OK {
				t.Fatalf("retry response = %+v, want success", second)
			}
			if len(provider.calls) != tc.wantCalls {
				t.Fatalf("RenameDurable calls = %d, want %d", len(provider.calls), tc.wantCalls)
			}
			if tc.wantCalls == 2 && provider.calls[0].RequestID != provider.calls[1].RequestID {
				t.Fatal("retry used a different request ID")
			}
			infos, err := fixture.client.List(context.Background())
			if err != nil || len(infos) != 1 || infos[0].Title != "canonical-retry" || infos[0].MetadataRevision != 1 {
				t.Fatalf("canonical after retry = %+v, %v", infos, err)
			}
		})
	}
}

func TestProductionRenameRetryReconcilesRemoteMetadataChanges(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failFirst error
	}{
		{name: "helper committed before production response loss", failFirst: base.ErrDisconnected},
		{name: "production committed before app response loss"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRemoteDaemonFixture(t)
			provider := &flakyMetadataProvider{
				DurableProvider: fixture.provider, DurableMetadataProvider: fixture.provider,
				DurableDescriptorLister: fixture.provider, DurableIdentityAttacher: fixture.provider, failFirst: tc.failFirst,
			}
			resolver := &recordingDurableResolver{provider: provider}
			production, _ := newRemoteRecoveryHarness(t, resolver)
			connected := connectRemoteSSHAsset(t, production)
			createResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "rename-remote-change-channel", "client-a")
			tabID := requireAttachedTabID(t, createResponse)
			rename := func() ipc.Response {
				return dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"`+tabID+`","title":"canonical-retry"}`, "", "client-a")
			}
			first := rename()
			if tc.failFirst != nil && first.OK {
				t.Fatalf("first unknown-result response = %+v, want failure", first)
			}
			infos, err := fixture.client.List(context.Background())
			if err != nil || len(infos) != 1 {
				t.Fatalf("initial infos = %+v, %v", infos, err)
			}
			if _, err := fixture.provider.RenameDurable(context.Background(), base.DurableRenameOptions{
				ID: tabID, Incarnation: infos[0].Incarnation, RequestID: ids.New(), ExpectedMetadataRevision: 1, Title: "other-canonical",
			}); err != nil {
				t.Fatal(err)
			}
			second := rename()
			infos, err = fixture.client.List(context.Background())
			if second.OK || second.Error == nil || second.Error.Code != ipc.Code("metadata_conflict") {
				t.Fatalf("metadata-changed retry = %+v, want metadata_conflict", second)
			}
			detail, ok := second.Error.Detail.(map[string]any)
			if !ok || detail["title"] != "other-canonical" || detail["metadataRevision"] != uint64(2) {
				t.Fatalf("conflict detail = %#v, want current canonical revision", detail)
			}
			third := rename()
			if !third.OK {
				t.Fatalf("same-title retry after conflict = %+v, want a new successful attempt", third)
			}
			wantCalls := 2
			if tc.failFirst != nil {
				wantCalls = 3
			}
			if len(provider.calls) != wantCalls {
				t.Fatalf("same-title retry calls = %v, want %d calls", provider.calls, wantCalls)
			}
			last := provider.calls[len(provider.calls)-1]
			if last.RequestID == provider.calls[0].RequestID || last.ExpectedMetadataRevision != 2 {
				t.Fatalf("same-title retry did not use a new current-revision attempt: calls=%v", provider.calls)
			}
			infos, err = fixture.client.List(context.Background())
			if err != nil || len(infos) != 1 || infos[0].Title != "canonical-retry" || infos[0].MetadataRevision != 3 {
				t.Fatalf("remote canonical after retry = %+v, %v", infos, err)
			}
		})
	}
}

func TestProductionPendingRenameAfterIncarnationChange(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	provider := &flakyMetadataProvider{DurableProvider: fixture.provider, DurableMetadataProvider: fixture.provider, DurableDescriptorLister: fixture.provider, DurableIdentityAttacher: fixture.provider, failFirst: base.ErrDisconnected}
	resolver := &recordingDurableResolver{provider: provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)
	createResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "pending-incarnation-channel", "client-a")
	tabID := requireAttachedTabID(t, createResponse)
	rename := func() ipc.Response {
		return dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"`+tabID+`","title":"stale-local"}`, "", "client-a")
	}
	first := rename()
	if first.OK {
		t.Fatalf("pending first response = %+v, want unknown-result error", first)
	}
	infos, err := fixture.client.List(context.Background())
	if err != nil || len(infos) != 1 {
		t.Fatalf("initial infos = %+v, %v", infos, err)
	}
	if err := fixture.provider.KillWithIdentity(context.Background(), tabID, base.DurableIdentity{CreatedAt: infos[0].CreatedAt, Incarnation: infos[0].Incarnation}); err != nil {
		t.Fatal(err)
	}
	replacement, err := fixture.provider.Create(context.Background(), base.DurableCreateOptions{ID: tabID, Command: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	_ = replacement.Close()
	second := rename()
	if !second.OK {
		t.Fatalf("pending retry after incarnation change = %+v, want new attempt success", second)
	}
	if len(provider.calls) != 2 || provider.calls[0].RequestID == provider.calls[1].RequestID ||
		provider.calls[0].Incarnation == provider.calls[1].Incarnation || provider.calls[1].ExpectedMetadataRevision != 0 {
		t.Fatalf("pending retry did not use a new zero-revision incarnation attempt: calls=%v", provider.calls)
	}
	infos, err = fixture.client.List(context.Background())
	if err != nil || len(infos) != 1 || infos[0].Title != "stale-local" || infos[0].MetadataRevision != 1 {
		t.Fatalf("pending retry canonical = %+v, %v", infos, err)
	}
}

func TestProductionRenameDurableConflictIncludesCurrentMetadata(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)
	createResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "rename-conflict-channel", "client-a")
	tabID := requireAttachedTabID(t, createResponse)
	infos, err := fixture.client.List(context.Background())
	if err != nil || len(infos) != 1 {
		t.Fatalf("fixture infos = %+v, %v", infos, err)
	}
	if _, err := fixture.provider.RenameDurable(context.Background(), base.DurableRenameOptions{
		ID: tabID, Incarnation: infos[0].Incarnation, RequestID: ids.New(), Title: "production-current",
	}); err != nil {
		t.Fatal(err)
	}

	response := dispatchDurableTest(t, production, "terminal_rename_tab", `{"tabId":"`+tabID+`","title":"stale-draft"}`, "", "client-a")
	if response.OK || response.Error == nil || response.Error.Code != ipc.Code("metadata_conflict") {
		t.Fatalf("stale rename response = %+v, want metadata_conflict", response)
	}
	detail, ok := response.Error.Detail.(map[string]any)
	if !ok || detail["title"] != "production-current" || detail["metadataRevision"] != uint64(1) {
		t.Fatalf("conflict detail = %#v, want current title and revision", response.Error.Detail)
	}
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil || tab.Info().Title != "production-current" {
		t.Fatalf("canonical tab after conflict = %+v, %v", tab.Info(), err)
	}
	identityErr := terminalIPCError(durable.ErrIdentity)
	appErr, ok := identityErr.(*ipc.Error)
	if !ok || appErr.Code != ipc.Code("identity_changed") {
		t.Fatalf("identity error mapping = %+v, want identity_changed", identityErr)
	}
}
