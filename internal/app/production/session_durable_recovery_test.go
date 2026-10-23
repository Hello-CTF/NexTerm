//go:build darwin || linux

package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/dbtest"
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

type unavailableCreateProvider struct{ calls int }

func (p *unavailableCreateProvider) Create(context.Context, base.DurableCreateOptions) (base.DurableAttachment, error) {
	p.calls++
	return nil, durable.ErrUnavailable
}

func (p *unavailableCreateProvider) Attach(context.Context, string) (base.DurableAttachment, error) {
	return nil, durable.ErrNotFound
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
		Connector:            fakeSSHConnector{},
		DurableResolver:      resolver,
		TerminalModeResolver: newProductionTerminalModeResolver(database),
		OpenAccess:           true,
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
	calls     int
}

func (r *transportDurableResolver) ResolveDurable(_ context.Context, transport base.Transport) (base.DurableProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	provider := r.providers[transport]
	if provider == nil {
		return nil, durable.ErrUnavailable
	}
	return provider, nil
}

func (r *transportDurableResolver) recorded() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
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

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a","mode":"kill"}`, "", "client-a"))
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

func TestProductionDefaultCloseDetachesRemoteTab(t *testing.T) {
	for _, kind := range []string{session.KindSSH, session.KindDevice} {
		t.Run(kind, func(t *testing.T) {
			fixture := newRemoteDaemonFixture(t)
			resolver := &recordingDurableResolver{provider: fixture.provider}
			production, _ := newRemoteRecoveryHarness(t, resolver)
			connected, err := production.Services.Sessions.Connect(t.Context(), session.Asset{
				ID: "default-close-" + kind, Name: kind, Kind: kind,
			})
			if err != nil {
				t.Fatal(err)
			}
			channelID := "default-close-channel-" + kind
			attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
			tabID := requireAttachedTabID(t, attachResponse)

			invalid := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","mode":"destroy"}`, "", "client-a")
			if invalid.OK || invalid.Error == nil || invalid.Error.Code != ipc.CodeBadParam {
				t.Fatalf("invalid close mode = %+v, want bad_param", invalid)
			}
			if _, err := production.Services.Sessions.Tab(tabID); err != nil {
				t.Fatalf("invalid close mode destroyed the tab: %v", err)
			}
			requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`"}`, "", "client-a"))
			tab, err := production.Services.Sessions.Tab(tabID)
			if err != nil {
				t.Fatalf("default close destroyed the remote tab: %v", err)
			}
			if info := tab.Info(); info.Exited || info.Subscribers != 0 {
				t.Fatalf("default close left tab = %+v, want a live detached tab", info)
			}
			descriptors, err := production.Services.Sessions.ListRemoteDurable(t.Context(), connected.ID)
			if err != nil {
				t.Fatal(err)
			}
			alive := false
			for _, descriptor := range descriptors {
				if descriptor.ID == tabID && !descriptor.Dead {
					alive = true
				}
			}
			if !alive {
				t.Fatalf("default close killed the remote process: %+v", descriptors)
			}

			reattachResponse := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","replayBytes":1024}`, channelID+"-again", "client-a")
			var reattached attachedTabDTO
			requireStoreTestResponse(t, reattachResponse, &reattached)
			if reattached.TabID != tabID || reattached.Exited {
				t.Fatalf("reattached tab = %+v, want original live tab %s", reattached, tabID)
			}
			requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","mode":"kill"}`, "", "client-a"))
			if _, err := production.Services.Sessions.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
				t.Fatalf("explicit kill kept tab: %v", err)
			}
		})
	}
}

func TestProductionDefaultCloseCleansAuthoritativelyExitedRemoteTab(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)
	channelID := "default-close-exited-channel"
	attachResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channelID, "client-a")
	tabID := requireAttachedTabID(t, attachResponse)
	writeDurableTestInput(t, production, tabID, "exit", "client-a")
	deadline := time.Now().Add(5 * time.Second)
	for {
		tab, err := production.Services.Sessions.Tab(tabID)
		if err == nil && tab.Info().Exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote tab did not report its authoritative exit: %+v, %v", tab, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`"}`, "", "client-a"))
	if _, err := production.Services.Sessions.Tab(tabID); !errors.Is(err, session.ErrTabNotFound) {
		t.Fatalf("authoritative exit cleanup kept tab: %v", err)
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

func TestProductionCloseDiscoveredSSHTabRequiresAuthorizedOwningSession(t *testing.T) {
	for _, mode := range []string{"detach", "kill"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRemoteDaemonFixture(t)
			other := newRemoteDaemonFixture(t)
			resolver := &transportDurableResolver{providers: make(map[base.Transport]base.DurableProvider)}
			production, factory := newRemoteRecoveryHarness(t, resolver)
			connected := connectRemoteSSHAsset(t, production)
			wrong := connectRemoteSSHAsset(t, production)
			for id, provider := range map[string]base.DurableProvider{connected.ID: fixture.provider, wrong.ID: other.provider} {
				transport, err := production.Services.Sessions.Transport(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				resolver.providers[transport] = provider
			}
			tabID := ids.New()
			directory := t.TempDir()
			beforePath := filepath.Join(directory, "before.pid")
			afterPath := filepath.Join(directory, "after.pid")
			attachment, err := fixture.provider.Create(t.Context(), base.DurableCreateOptions{ID: tabID, Command: []string{"sh"}, Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := attachment.Write([]byte("printf '%s' \"$$\" > '" + beforePath + "'; printf 'discovered-''ready\\n'\n")); err != nil {
				t.Fatal(err)
			}
			readUntilDurableOutput(t, attachment, "discovered-ready")
			if err := attachment.Close(); err != nil {
				t.Fatal(err)
			}
			beforePID, err := os.ReadFile(beforePath)
			if err != nil || len(beforePID) == 0 {
				t.Fatalf("original shell PID = %q, %v", beforePID, err)
			}
			args := `{"tabId":"` + tabID + `","sessionId":"` + connected.ID + `","mode":"` + mode + `"}`
			denied := production.Dispatcher.Dispatch(ipc.WithUserID(t.Context(), "another-account"), ipc.Request{
				Command: "terminal_close_tab", Args: []byte(args), ClientID: "client-other",
			}, production.Environment("client-other"))
			if denied.OK || denied.Error == nil || denied.Error.Code != ipc.CodeForbidden {
				t.Fatalf("unauthorized remote kill = %+v, want forbidden", denied)
			}
			missing := dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+wrong.ID+`","mode":"`+mode+`"}`, "", "client-a")
			if missing.OK || missing.Error == nil || missing.Error.Code != ipc.CodeNotFound {
				t.Fatalf("wrong daemon kill = %+v, want not_found", missing)
			}
			infos, err := fixture.client.List(t.Context())
			if err != nil || len(infos) != 1 || infos[0].Dead {
				t.Fatalf("rejected kill disturbed original shell: %+v, %v", infos, err)
			}
			closed := dispatchDurableTest(t, production, "terminal_close_tab", args, "", "client-a")
			if mode == "detach" {
				if closed.OK || closed.Error == nil || closed.Error.Code != ipc.CodeNotFound {
					t.Fatalf("local detach with unknown remote survival = %+v, want visible not_found", closed)
				}
				if calls := resolver.recorded(); calls != 0 {
					t.Fatalf("local detach triggered %d helper resolutions, want none", calls)
				}
			} else {
				requireProductionNull(t, closed)
			}
			if mode == "detach" {
				descriptors, err := production.Services.Sessions.ListRemoteDurable(t.Context(), connected.ID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, descriptor := range descriptors {
					if descriptor.ID == tabID && !descriptor.Dead {
						found = true
					}
				}
				if !found {
					t.Fatal("detached original shell cannot be discovered")
				}
				channel := "discovered-detach-recover"
				response := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","replayBytes":262144}`, channel, "client-a")
				requireAttachedTabID(t, response)
				writeDurableTestInput(t, production, tabID, "printf '%s' \"$$\" > '"+afterPath+"'; printf 'discovered-''continued\\n'", "client-a")
				waitForProductionOutput(t, factory.at(channel, 0), "discovered-continued")
				afterPID, err := os.ReadFile(afterPath)
				if err != nil || !bytes.Equal(beforePID, afterPID) {
					t.Fatalf("detached shell PID = %q, %v; want %q", afterPID, err, beforePID)
				}
				requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","sessionId":"`+connected.ID+`","mode":"kill"}`, "", "client-a"))
			}

			infos, err = fixture.client.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, info := range infos {
				if info.ID == tabID && !info.Dead {
					t.Fatalf("explicit remote kill kept shell alive: %+v", info)
				}
			}
		})
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

func TestProductionTerminalAttachFallsBackOnceWhenHelperUnavailable(t *testing.T) {
	resolver := &recordingDurableResolver{err: durable.ErrUnavailable}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	plainResponse := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":false}`, "plain-ssh-channel", "client-a")
	plainTabID := requireAttachedTabID(t, plainResponse)
	plainTab, err := production.Services.Sessions.Tab(plainTabID)
	if err != nil {
		t.Fatal(err)
	}
	if plainTab.Info().Durable {
		t.Fatal("explicit volatile SSH attach became durable")
	}

	for index := range 2 {
		response := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, fmt.Sprintf("fallback-ssh-channel-%d", index), "client-a")
		var attached attachedTabDTO
		requireStoreTestResponse(t, response, &attached)
		if attached.FallbackNotice == "" && index == 0 || attached.FallbackNotice != "" && index == 1 {
			t.Fatalf("fallback notice on attempt %d = %q", index, attached.FallbackNotice)
		}
		tab, err := production.Services.Sessions.Tab(attached.TabID)
		if err != nil || tab.Info().Durable {
			t.Fatalf("fallback tab = %+v, %v; want ordinary PTY", tab, err)
		}
	}
	if calls := len(resolver.recorded()); calls != 1 {
		t.Fatalf("helper resolution calls = %d; want one per session", calls)
	}
	strict := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "strict-ssh-channel", "client-a")
	if strict.OK || strict.Error == nil || strict.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("explicit durable attach = %+v; want unsupported", strict)
	}
	if tabs := production.Services.Sessions.ListTabs(); len(tabs) != 3 {
		t.Fatalf("tabs after fallback and strict failure = %+v", tabs)
	}
}

func TestProductionTerminalAttachFallsBackWhenDurableCreateBecomesUnavailable(t *testing.T) {
	provider := &unavailableCreateProvider{}
	resolver := &recordingDurableResolver{provider: provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	connected := connectRemoteSSHAsset(t, production)

	for index := range 2 {
		response := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, fmt.Sprintf("create-fallback-%d", index), "client-a")
		var attached attachedTabDTO
		requireStoreTestResponse(t, response, &attached)
		if (attached.FallbackNotice != "") != (index == 0) {
			t.Fatalf("attempt %d fallback notice = %q", index, attached.FallbackNotice)
		}
		tab, err := production.Services.Sessions.Tab(attached.TabID)
		if err != nil || tab.Info().Durable {
			t.Fatalf("attempt %d tab = %+v, %v; want ordinary PTY", index, tab, err)
		}
	}
	if provider.calls != 1 || len(resolver.recorded()) != 1 {
		t.Fatalf("durable Create calls = %d, resolver calls = %d; want one each", provider.calls, len(resolver.recorded()))
	}
	strict := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "create-strict", "client-a")
	if strict.OK || strict.Error == nil || strict.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("explicit durable attach = %+v; want unsupported", strict)
	}
	if provider.calls != 2 {
		t.Fatalf("explicit durable Create calls = %d; want a retry after automatic fallback", provider.calls)
	}
}

func TestProductionDefaultTerminalModeAndAssetOverride(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	preferences := account.NewPreferences(production.Services.Store)
	if _, err := preferences.UpdateDefaults(t.Context(), map[string]json.RawMessage{"terminal.defaultMode": json.RawMessage(`"regular"`)}, nil); err != nil {
		t.Fatal(err)
	}
	connected := connectRemoteSSHAsset(t, production)
	response := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "default-regular", "client-a")
	var attached attachedTabDTO
	requireStoreTestResponse(t, response, &attached)
	tab, err := production.Services.Sessions.Tab(attached.TabID)
	if err != nil || tab.Info().Durable || len(resolver.recorded()) != 0 {
		t.Fatalf("regular default started helper or durable tab: info=%+v err=%v", tab, err)
	}
	info, err := production.Services.Sessions.Session(connected.ID)
	if err != nil {
		t.Fatal(err)
	}
	durableOptions := `{"terminalMode":"durable"}`
	if _, err := production.Services.Store.AssetUpdate(t.Context(), info.Asset().ID, store.AssetPatch{OptionsJSON: &durableOptions}); err != nil {
		t.Fatal(err)
	}
	response = dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "asset-durable", "client-a")
	requireStoreTestResponse(t, response, &attached)
	tab, err = production.Services.Sessions.Tab(attached.TabID)
	if err != nil || !tab.Info().Durable || len(resolver.recorded()) != 1 {
		t.Fatalf("asset durable mode failed to override regular default: info=%+v err=%v", tab, err)
	}
}

func TestProductionTerminalModePreferenceOwnerOverride(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	owner := ipc.WithUserID(t.Context(), "terminal-owner")
	asset, err := database.AssetCreate(owner, store.AssetInput{Kind: "ssh", Name: "inherits-default", OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	preferences := account.NewPreferences(database)
	if _, err := preferences.UpdateDefaults(owner, map[string]json.RawMessage{"terminal.defaultMode": json.RawMessage(`"regular"`)}, nil); err != nil {
		t.Fatal(err)
	}
	resolve := newProductionTerminalModeResolver(database)
	if got, err := resolve(owner, asset.ID); err != nil || got != session.TerminalModeRegular {
		t.Fatalf("default mode = %q, %v", got, err)
	}
	if _, err := preferences.UpdateUserOverrides(owner, "terminal-owner", map[string]json.RawMessage{"terminal.defaultMode": json.RawMessage(`"durable"`)}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(owner, asset.ID); err != nil || got != session.TerminalModeAuto {
		t.Fatalf("owner durable preference = %q, %v", got, err)
	}
	regularOptions := `{"terminalMode":"regular"}`
	if _, err := database.AssetUpdate(owner, asset.ID, store.AssetPatch{OptionsJSON: &regularOptions}); err != nil {
		t.Fatal(err)
	}
	if got, err := resolve(owner, asset.ID); err != nil || got != session.TerminalModeRegular {
		t.Fatalf("asset regular override = %q, %v", got, err)
	}
}

func TestProductionRegularSSHModeOnlyAffectsNewTabs(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	resolver := &recordingDurableResolver{provider: fixture.provider}
	production, _ := newRemoteRecoveryHarness(t, resolver)
	host, username, port := "127.0.0.1", "tester", int32(22)
	asset, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
		Kind: "ssh", Name: "regular-terminal", Host: &host, Username: &username, Port: &port,
		OptionsJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var connected sessionInfoDTO
	requireStoreTestResponse(t, dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+asset.ID+`"}}`, "", "client-a"), &connected)
	regularOptions := `{"terminalMode":"regular"}`
	if _, err := production.Services.Store.AssetUpdate(t.Context(), asset.ID, store.AssetPatch{OptionsJSON: &regularOptions}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"terminal_attach", "terminal_create_tab"} {
		channel := ""
		if command == "terminal_attach" {
			channel = "regular-ssh-channel"
		}
		response := dispatchDurableTest(t, production, command, `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, channel, "client-a")
		var attached attachedTabDTO
		requireStoreTestResponse(t, response, &attached)
		tab, err := production.Services.Sessions.Tab(attached.TabID)
		if err != nil || tab.Info().Durable || attached.FallbackNotice != "" {
			t.Fatalf("%s tab = %+v, response = %+v, err = %v; want ordinary PTY without fallback", command, tab, attached, err)
		}
	}
	if calls := len(resolver.recorded()); calls != 0 {
		t.Fatalf("helper resolution calls = %d; regular mode must not start helper for new tabs", calls)
	}
	forced := dispatchDurableTest(t, production, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24,"durable":true}`, "regular-forced-channel", "client-a")
	if forced.OK || forced.Error == nil || forced.Error.Code != ipc.CodeUnsupported || !strings.Contains(forced.Error.Message, "普通终端") {
		t.Fatalf("explicit durable attach in regular mode = %+v", forced)
	}
	if len(resolver.recorded()) != 0 {
		t.Fatal("explicit durable attach started helper despite regular mode")
	}
	tabID := createDaemonTab(t, fixture.provider, "regular-mode-old-durable")
	listResponse := dispatchDurableTest(t, production, "terminal_list", `{"sessionId":"`+connected.ID+`","durable":true}`, "", "client-a")
	var descriptors []durableDescriptorDTO
	requireStoreTestResponse(t, listResponse, &descriptors)
	if len(descriptors) != 1 || descriptors[0].ID != tabID || descriptors[0].Dead {
		t.Fatalf("regular mode hid the old durable tab: %+v", descriptors)
	}
	recovery := dispatchDurableTest(t, production, "terminal_attach_tab", `{"sessionId":"`+connected.ID+`","tabId":"`+tabID+`"}`, "old-durable-channel", "client-a")
	var recovered attachedTabDTO
	requireStoreTestResponse(t, recovery, &recovered)
	if recovered.TabID != tabID {
		t.Fatalf("regular mode recovered tab %q, want original %q", recovered.TabID, tabID)
	}
	tab, err := production.Services.Sessions.Tab(tabID)
	if err != nil || !tab.Info().Durable {
		t.Fatalf("regular mode changed the recovered durable tab: %+v, %v", tab, err)
	}
	if calls := len(resolver.recorded()); calls != 1 {
		t.Fatalf("old durable list/recovery used %d resolver calls, want one cached provider resolution", calls)
	}
}

func TestProductionDefaultSSHTabsSurviveAppRestartWithOriginalProcesses(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("ENV", "")
	fixture := newRemoteDaemonFixture(t)
	connect := func(production *Production) sessionInfoDTO {
		host, user, port := "127.0.0.1", "root", int32(22)
		asset, err := production.Services.Store.AssetCreate(t.Context(), store.AssetInput{
			Kind: "ssh", Name: "restart-env-startup", Host: &host, Username: &user, Port: &port,
			OptionsJSON: `{"env":{"NEXTERM_ASSET_ENV":"preserved"},"encoding":"gbk","startupCommand":"NEXTERM_STARTUP_COUNT=$(( ${NEXTERM_STARTUP_COUNT:-0} + 1 ))"}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		var connected sessionInfoDTO
		requireStoreTestResponse(t, dispatchDurableTest(t, production, "session_connect", `{"args":{"assetId":"`+asset.ID+`"}}`, "", "client-a"), &connected)
		return connected
	}
	first, firstFactory := newRemoteRecoveryHarness(t, &recordingDurableResolver{provider: fixture.provider})
	connected := connect(first)
	directory := t.TempDir()
	tabIDs := make([]string, 0, 2)
	pids := make([]string, 0, 2)
	for _, name := range []string{"one", "two"} {
		response := dispatchDurableTest(t, first, "terminal_attach", `{"sessionId":"`+connected.ID+`","cols":80,"rows":24}`, "restart-first-"+name, "client-a")
		tabID := requireAttachedTabID(t, response)
		tabIDs = append(tabIDs, tabID)
		pidPath := filepath.Join(directory, name+"-before.pid")
		writeDurableTestInput(t, first, tabID, "NEXTERM_RESTART_STATE="+name+"; printf '%s' \"$$\" > '"+pidPath+"'; printf 'restart-''ready-"+name+"\\n'", "client-a")
		waitForProductionOutput(t, firstFactory.at("restart-first-"+name, 0), "restart-ready-"+name)
		pid, err := os.ReadFile(pidPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(pid) == 0 {
			t.Fatal("shell did not report its PID")
		}
		pids = append(pids, string(pid))
	}
	before, err := fixture.client.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, secondFactory := newRemoteRecoveryHarness(t, &recordingDurableResolver{provider: fixture.provider})
	reconnected := connect(second)
	response := dispatchDurableTest(t, second, "terminal_list", `{"sessionId":"`+reconnected.ID+`","durable":true}`, "", "client-b")
	var descriptors []durableDescriptorDTO
	requireStoreTestResponse(t, response, &descriptors)
	for index, name := range []string{"one", "two"} {
		tabID := tabIDs[index]
		found := false
		for _, descriptor := range descriptors {
			if descriptor.ID == tabID && !descriptor.Dead {
				found = true
			}
		}
		if !found {
			t.Fatalf("original tab %s missing after app shutdown: %+v", tabID, descriptors)
		}
		channelID := "restart-second-" + name
		attached := dispatchDurableTest(t, second, "terminal_attach_tab", `{"sessionId":"`+reconnected.ID+`","tabId":"`+tabID+`","replayBytes":262144}`, channelID, "client-b")
		var info attachedTabDTO
		requireStoreTestResponse(t, attached, &info)
		if info.TabID != tabID || info.Encoding != "gbk" {
			t.Fatalf("restored tab = %+v, want original tab with gbk encoding", info)
		}
		stream := secondFactory.at(channelID, 0)
		waitForProductionOutput(t, stream, "restart-ready-"+name)
		pidPath := filepath.Join(directory, name+"-after.pid")
		writeDurableTestInput(t, second, tabID, "printf '%s' \"$$\" > '"+pidPath+"'; printf 'restart-''continued:%s:%s:%s\\n' \"$NEXTERM_RESTART_STATE\" \"$NEXTERM_ASSET_ENV\" \"$NEXTERM_STARTUP_COUNT\"", "client-b")
		waitForProductionOutput(t, stream, "restart-continued:"+name+":preserved:1")
		pid, err := os.ReadFile(pidPath)
		if err != nil || string(pid) != pids[index] {
			t.Fatalf("restored shell PID = %q, %v; want original %s", pid, err, pids[index])
		}
	}
	after, err := fixture.client.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, original := range before {
		for _, restored := range after {
			if original.ID == restored.ID && original.Incarnation != restored.Incarnation {
				t.Fatalf("restored tab changed incarnation: before=%+v after=%+v", original, restored)
			}
		}
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

func TestProductionTwoManagersDiscoverAndShareDefaultDurableSSH(t *testing.T) {
	t.Setenv("SHELL", "")
	t.Setenv("ENV", "")
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
	createResponse := dispatchDurableTest(t, productionA, "terminal_attach", `{"sessionId":"`+connectedA.ID+`","cols":80,"rows":24}`, channelA, "client-a")
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

	requireProductionNull(t, dispatchDurableTest(t, productionC, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-c","mode":"kill"}`, "", "client-c"))
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
	unscoped := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":262144}`, "ambiguous-durable-channel", "client-a")
	if unscoped.OK || unscoped.Error == nil || unscoped.Error.Code != ipc.CodeBadParam || !strings.Contains(unscoped.Error.Message, "指定会话") {
		t.Fatalf("unscoped duplicate attach = %+v", unscoped)
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
	unscopedInMemory := dispatchDurableTest(t, production, "terminal_attach_tab", `{"tabId":"`+tabID+`","replayBytes":1024}`, "ambiguous-in-memory-channel", "client-b")
	if unscopedInMemory.OK || unscopedInMemory.Error == nil || unscopedInMemory.Error.Code != ipc.CodeBadParam || !strings.Contains(unscopedInMemory.Error.Message, "指定会话") {
		t.Fatalf("unscoped in-memory duplicate attach = %+v", unscopedInMemory)
	}
	requireProductionNull(t, dispatchDurableTest(t, production, "terminal_close_tab", `{"tabId":"`+tabID+`","clientId":"client-a","mode":"kill"}`, "", "client-a"))
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
