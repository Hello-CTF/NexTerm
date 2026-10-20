package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/ssh"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

type broadcastGateChannel struct {
	id     string
	closed chan struct{}
	once   sync.Once
	mu     sync.Mutex
	buffer bytes.Buffer
}

func newBroadcastGateChannel(id string) *broadcastGateChannel {
	return &broadcastGateChannel{id: id, closed: make(chan struct{})}
}

func (c *broadcastGateChannel) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *broadcastGateChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.Write(data)
}

func (c *broadcastGateChannel) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *broadcastGateChannel) Stderr() io.Reader                            { return nil }
func (c *broadcastGateChannel) Resize(context.Context, uint32, uint32) error { return nil }
func (c *broadcastGateChannel) Wait(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *broadcastGateChannel) CloseWrite() error  { return nil }
func (c *broadcastGateChannel) ID() string         { return c.id }
func (c *broadcastGateChannel) Generation() uint64 { return 1 }

func (c *broadcastGateChannel) written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.String()
}

type broadcastGateTransport struct {
	kind       string
	generation uint64
	alive      atomic.Bool
	mu         sync.Mutex
	channels   []*broadcastGateChannel
}

func newBroadcastGateTransport(kind string, generation uint64) *broadcastGateTransport {
	transport := &broadcastGateTransport{kind: kind, generation: generation}
	transport.alive.Store(true)
	return transport
}

func (t *broadcastGateTransport) Kind() string       { return t.kind }
func (t *broadcastGateTransport) Generation() uint64 { return t.generation }
func (t *broadcastGateTransport) IsAlive() bool      { return t.alive.Load() }
func (t *broadcastGateTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}
func (t *broadcastGateTransport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, nil
}
func (t *broadcastGateTransport) Close() error {
	t.alive.Store(false)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, channel := range t.channels {
		_ = channel.Close()
	}
	return nil
}
func (t *broadcastGateTransport) OpenPTY(context.Context, base.PTYOptions) (base.Channel, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	channel := newBroadcastGateChannel(fmt.Sprintf("broadcast-channel-%d", len(t.channels)+1))
	t.channels = append(t.channels, channel)
	return channel, nil
}
func (t *broadcastGateTransport) firstChannel() *broadcastGateChannel {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.channels) == 0 {
		return nil
	}
	return t.channels[0]
}

type broadcastGateConnector struct {
	mu         sync.Mutex
	transports map[string]*broadcastGateTransport
	dials      atomic.Int32
	connectErr func(assetID string, call int32) error
}

func (c *broadcastGateConnector) Connect(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
	call := c.dials.Add(1)
	if c.connectErr != nil {
		if err := c.connectErr(asset.ID, call); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	transport := newBroadcastGateTransport(asset.Kind, generation)
	c.transports[asset.ID] = transport
	return transport, nil
}

func (c *broadcastGateConnector) transport(assetID string) *broadcastGateTransport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transports[assetID]
}

type broadcastGateTarget struct {
	assetID   string
	sessionID string
	tabID     string
}

type broadcastGateFixture struct {
	ctx        context.Context
	database   *store.Store
	manager    *session.Manager
	dispatcher *ipc.Dispatcher
	connector  *broadcastGateConnector
	service    *terminalCommandService
	production broadcastGateTarget
	staging    broadcastGateTarget
}

func newBroadcastGateFixture(t *testing.T) *broadcastGateFixture {
	t.Helper()
	ctx := ipc.WithUserID(t.Context(), "broadcast-owner")
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connector := &broadcastGateConnector{transports: make(map[string]*broadcastGateTransport)}
	manager := session.NewManager(session.Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	service := newTerminalCommandService(database, manager, nil, nil, nil, nil, nil, nil, false)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerSession(dispatcher); err != nil {
		t.Fatal(err)
	}
	fixture := &broadcastGateFixture{ctx: ctx, database: database, manager: manager, dispatcher: dispatcher, connector: connector, service: service}
	fixture.production = fixture.openTarget(t, "Production API", "api.production.example", "env:production")
	fixture.staging = fixture.openTarget(t, "Staging API", "api.staging.example", "env:staging")
	return fixture
}

func (f *broadcastGateFixture) openTarget(t *testing.T, name, hostname, tags string) broadcastGateTarget {
	t.Helper()
	asset, err := f.database.AssetCreate(f.ctx, store.AssetInput{Kind: session.KindSSH, Name: name, Host: &hostname, Tags: tags})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := f.manager.ConnectFor(f.ctx, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := f.manager.OpenTabFor(f.ctx, session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "broadcast-" + asset.ID, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return broadcastGateTarget{assetID: asset.ID, sessionID: connected.ID, tabID: tab.ID}
}

func dispatchBroadcastTest(dispatcher *ipc.Dispatcher, ctx context.Context, command string, input any) ipc.Response {
	payload, err := json.Marshal(map[string]any{"args": input})
	if err != nil {
		panic(err)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: payload}, ipc.Environment{})
}

func broadcastTestInts(data string) []int {
	values := make([]int, len([]byte(data)))
	for index, value := range []byte(data) {
		values[index] = int(value)
	}
	return values
}

func (f *broadcastGateFixture) prepare(tabIDs []string, data string) ipc.Response {
	return dispatchBroadcastTest(f.dispatcher, f.ctx, "terminal_broadcast_prepare", terminalBroadcastPrepareRequest{
		TabIDs: tabIDs, Data: broadcastTestInts(data),
	})
}

func (f *broadcastGateFixture) write(tabID, data string, broadcast *terminalBroadcastWriteOptions) ipc.Response {
	return dispatchBroadcastTest(f.dispatcher, f.ctx, "terminal_write", terminalWriteRequest{
		TabID: tabID, Data: broadcastTestInts(data), ClientID: "client-a", Broadcast: broadcast,
	})
}

func requireBroadcastConfirmation(t *testing.T, response ipc.Response) broadcastConfirmationRequiredDetail {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("response = %+v, want needs_confirm", response)
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail broadcastConfirmationRequiredDetail
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Kind != broadcastConfirmationRequiredKind || len(detail.ProductionTargets) == 0 {
		t.Fatalf("confirmation detail = %+v", detail)
	}
	return detail
}

func requireBroadcastOK(t *testing.T, response ipc.Response) {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
}

func TestBroadcastProductionIPCRequiresValidConfirmation(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	tabIDs := []string{fixture.production.tabID, fixture.staging.tabID}
	const input = "deploy --secret=broadcast-sensitive\n"
	prepare := fixture.prepare(tabIDs, input)
	detail := requireBroadcastConfirmation(t, prepare)
	encodedError, err := json.Marshal(prepare.Error)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedError), "broadcast-sensitive") || strings.Contains(string(encodedError), "deploy --secret") {
		t.Fatalf("confirmation error contains input plaintext: %s", encodedError)
	}
	if len(detail.ProductionTargets) != 1 || detail.ProductionTargets[0].Hostname != "api.production.example" {
		t.Fatalf("production targets = %+v", detail.ProductionTargets)
	}
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != "" {
		t.Fatalf("preflight wrote %q", got)
	}

	requireBroadcastConfirmation(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{TargetTabIDs: tabIDs}))
	requireBroadcastConfirmation(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: strings.Repeat("0", 64), InputHash: strings.Repeat("1", 64),
		},
	}))
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != "" {
		t.Fatalf("unconfirmed writes reached the channel: %q", got)
	}

	confirmation := &broadcastProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, InputHash: detail.InputHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("required environment names = %v", detail.RequiredEnvironmentNames)
	}
	requireBroadcastConfirmation(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: detail.TargetSetHash, InputHash: detail.InputHash,
			EnvironmentNames: []string{"staging"},
		},
	}))
	requireBroadcastConfirmation(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: false, TargetSetHash: detail.TargetSetHash, InputHash: detail.InputHash,
			EnvironmentNames: detail.RequiredEnvironmentNames,
		},
	}))
	requireBroadcastOK(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs, ProductionConfirmation: confirmation,
	}))
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != input {
		t.Fatalf("confirmed channel input = %q, want %q", got, input)
	}

	requireBroadcastConfirmation(t, fixture.write(fixture.production.tabID, "deploy --safe\n", &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs, ProductionConfirmation: confirmation,
	}))
	requireBroadcastConfirmation(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: []string{fixture.production.tabID}, ProductionConfirmation: confirmation,
	}))
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != input {
		t.Fatalf("stale confirmation appended input: %q", got)
	}
	rechallenge := requireBroadcastConfirmation(t, fixture.prepare(tabIDs, "deploy --safe\n"))
	if rechallenge.InputHash == detail.InputHash {
		t.Fatal("editing input did not change the challenge")
	}

	kind := broadcastConfirmationRequiredKind
	audits, err := fixture.database.AuditQuery(fixture.ctx, store.AuditQuery{Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 {
		t.Fatalf("confirmation audits = %+v, want exactly one", audits)
	}
	if strings.Contains(audits[0].PayloadJSON, "broadcast-sensitive") || strings.Contains(audits[0].PayloadJSON, "deploy --secret") {
		t.Fatalf("audit contains input plaintext: %s", audits[0].PayloadJSON)
	}
}

func TestBroadcastProductionIPCKeepsNonProductionAndOrdinaryInputDirect(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	tabIDs := []string{fixture.production.tabID, fixture.staging.tabID}
	requireBroadcastOK(t, fixture.prepare([]string{fixture.staging.tabID}, "hostname\n"))
	requireBroadcastOK(t, fixture.write(fixture.staging.tabID, "hostname\n", &terminalBroadcastWriteOptions{
		TargetTabIDs: []string{fixture.staging.tabID},
	}))
	if got := fixture.connector.transport(fixture.staging.assetID).firstChannel().written(); got != "hostname\n" {
		t.Fatalf("non-production broadcast input = %q", got)
	}

	// A write from an ordinary terminal into a target set containing production is
	// challenged just like a write originating from the production terminal.
	mixedDetail := requireBroadcastConfirmation(t, fixture.write(fixture.staging.tabID, "mixed\n", &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
	}))
	if got := fixture.connector.transport(fixture.staging.assetID).firstChannel().written(); got != "hostname\n" {
		t.Fatalf("challenged write reached the channel before confirmation: %q", got)
	}
	requireBroadcastOK(t, fixture.write(fixture.staging.tabID, "mixed\n", &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: mixedDetail.TargetSetHash, InputHash: mixedDetail.InputHash,
			EnvironmentNames: mixedDetail.RequiredEnvironmentNames,
		},
	}))
	if got := fixture.connector.transport(fixture.staging.assetID).firstChannel().written(); got != "hostname\nmixed\n" {
		t.Fatalf("confirmed cross-environment broadcast input = %q", got)
	}

	requireBroadcastOK(t, fixture.write(fixture.production.tabID, "ordinary\n", nil))
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != "ordinary\n" {
		t.Fatalf("ordinary production input = %q", got)
	}

	connected, err := fixture.manager.ConnectFor(fixture.ctx, session.Asset{ID: "synthetic-local", Name: "Local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	localTab, err := fixture.manager.OpenTabFor(fixture.ctx, session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "broadcast-synthetic-local", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	requireBroadcastOK(t, fixture.prepare([]string{localTab.ID}, "local\n"))
	requireBroadcastOK(t, fixture.write(localTab.ID, "local\n", &terminalBroadcastWriteOptions{TargetTabIDs: []string{localTab.ID}}))
	if got := fixture.connector.transport("synthetic-local").firstChannel().written(); got != "local\n" {
		t.Fatalf("synthetic local broadcast input = %q", got)
	}

	kind := broadcastConfirmationRequiredKind
	count, err := fixture.database.AuditCount(fixture.ctx, store.AuditQuery{Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("direct paths recorded %d production confirmations, want exactly one", count)
	}
}

func TestBroadcastProductionIPCRejectsStaleIdentityAfterRetag(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	tabIDs := []string{fixture.production.tabID, fixture.staging.tabID}
	const input = "deploy --retag-sensitive\n"
	detail := requireBroadcastConfirmation(t, fixture.prepare(tabIDs, input))
	confirmation := &broadcastProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, InputHash: detail.InputHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}

	// Re-tagging the asset changes its resolved identity: the old confirmation no
	// longer binds the current target set and must be rejected as stale.
	stagingTags := "env:staging"
	if _, err := fixture.database.AssetUpdate(fixture.ctx, fixture.production.assetID, store.AssetPatch{Tags: &stagingTags}); err != nil {
		t.Fatal(err)
	}
	stale := fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs, ProductionConfirmation: confirmation,
	})
	if stale.OK || stale.Error == nil || stale.Error.Code != ipc.Code("conflict") {
		t.Fatalf("stale confirmation response = %+v, want conflict", stale)
	}
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != "" {
		t.Fatalf("stale confirmation survived an identity change: %q", got)
	}
	requireBroadcastOK(t, fixture.prepare(tabIDs, input))

	// Re-tagging back to production challenges again; the target-set hash is
	// deterministic over the resolved identity, so it matches the original
	// challenge and the freshly obtained challenge confirms cleanly.
	productionTags := "env:production"
	if _, err := fixture.database.AssetUpdate(fixture.ctx, fixture.production.assetID, store.AssetPatch{Tags: &productionTags}); err != nil {
		t.Fatal(err)
	}
	rechallenge := requireBroadcastConfirmation(t, fixture.prepare(tabIDs, input))
	if rechallenge.TargetSetHash != detail.TargetSetHash {
		t.Fatal("restored identity produced a different target-set hash")
	}
	requireBroadcastOK(t, fixture.write(fixture.production.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: rechallenge.TargetSetHash, InputHash: rechallenge.InputHash,
			EnvironmentNames: rechallenge.RequiredEnvironmentNames,
		},
	}))
	if got := fixture.connector.transport(fixture.production.assetID).firstChannel().written(); got != input {
		t.Fatalf("reconfirmed channel input = %q, want %q", got, input)
	}
}

func TestBroadcastProductionIPCHonorsConfiguredProductionEnvironments(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	if err := guard.SetProductionEnvironments(fixture.ctx, fixture.database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	live := fixture.openTarget(t, "Live API", "api.live.example", "env:live")
	tabIDs := []string{live.tabID, fixture.staging.tabID}
	const input = "deploy --live\n"
	detail := requireBroadcastConfirmation(t, fixture.prepare(tabIDs, input))
	if len(detail.ProductionTargets) != 1 || detail.ProductionTargets[0].Hostname != "api.live.example" {
		t.Fatalf("production targets = %+v", detail.ProductionTargets)
	}
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "live" {
		t.Fatalf("required environment names = %v", detail.RequiredEnvironmentNames)
	}
	requireBroadcastConfirmation(t, fixture.write(live.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: detail.TargetSetHash, InputHash: detail.InputHash,
			EnvironmentNames: []string{"production"},
		},
	}))
	requireBroadcastOK(t, fixture.write(live.tabID, input, &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: detail.TargetSetHash, InputHash: detail.InputHash,
			EnvironmentNames: []string{"live"},
		},
	}))
	if got := fixture.connector.transport(live.assetID).firstChannel().written(); got != input {
		t.Fatalf("confirmed live channel input = %q, want %q", got, input)
	}

	// Built-in production names stay guarded regardless of the configuration.
	productionDetail := requireBroadcastConfirmation(t, fixture.prepare([]string{fixture.production.tabID, live.tabID}, input))
	if len(productionDetail.RequiredEnvironmentNames) != 2 ||
		productionDetail.RequiredEnvironmentNames[0] != "live" || productionDetail.RequiredEnvironmentNames[1] != "production" {
		t.Fatalf("combined required environment names = %v", productionDetail.RequiredEnvironmentNames)
	}
}

func TestBroadcastConnectsDroppedSessionWithOptIn(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	fixture.service.hostKeys = newBroadcastFixtureHostKeys(t, fixture)
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.production.example", 22, "ssh-ed25519", "SHA256:production"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.staging.example", 22, "ssh-ed25519", "SHA256:staging"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Disconnect(fixture.production.sessionID); err != nil {
		t.Fatal(err)
	}

	// Default: no opt-in keeps the pre-R-07 behavior and never dials.
	baseline := fixture.connector.dials.Load()
	tabIDs := []string{fixture.production.tabID, fixture.staging.tabID}
	detail := requireBroadcastConfirmation(t, fixture.prepare(tabIDs, "hostname\n"))
	if len(detail.ProductionTargets) != 1 || detail.ProductionTargets[0].TabID != fixture.production.tabID {
		t.Fatalf("production targets = %+v", detail.ProductionTargets)
	}
	if fixture.connector.dials.Load() != baseline {
		t.Fatalf("default resolve dialed %d times", fixture.connector.dials.Load()-baseline)
	}

	// With the broadcast opt-in the dropped session is reconnected exactly once;
	// the R-01 typed-name confirmation still applies to the production target.
	if err := fixture.database.SettingSet(fixture.ctx, BroadcastConnectOptInSettingKeyForUser(fixture.ctx), "true"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.SettingSet(fixture.ctx, BroadcastConnectProductionOptInSettingKeyForUser(fixture.ctx), "true"); err != nil {
		t.Fatal(err)
	}
	rechallenge := requireBroadcastConfirmation(t, fixture.prepare(tabIDs, "hostname\n"))
	if fixture.connector.dials.Load() != baseline+1 {
		t.Fatalf("opt-in resolve dialed %d times, want 1", fixture.connector.dials.Load()-baseline)
	}
	requireBroadcastOK(t, fixture.write(fixture.production.tabID, "hostname\n", &terminalBroadcastWriteOptions{
		TargetTabIDs: tabIDs,
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: rechallenge.TargetSetHash, InputHash: rechallenge.InputHash,
			EnvironmentNames: rechallenge.RequiredEnvironmentNames,
		},
	}))
	connected, err := fixture.manager.SessionFor(fixture.ctx, fixture.production.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if connected.Info().Status != session.StatusConnected {
		t.Fatalf("reconnected session status = %q", connected.Info().Status)
	}

	// Already-connected targets are never dialed.
	before := fixture.connector.dials.Load()
	requireBroadcastConfirmation(t, fixture.prepare(tabIDs, "hostname\n"))
	if fixture.connector.dials.Load() != before {
		t.Fatalf("connected resolve redialed: %d dials", fixture.connector.dials.Load())
	}
}

func TestBroadcastConnectGates(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	fixture.service.hostKeys = newBroadcastFixtureHostKeys(t, fixture)
	live := fixture.openTarget(t, "Live API", "api.live.example", "env:live")
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.live.example", 22, "ssh-ed25519", "SHA256:live"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.SettingSet(fixture.ctx, BroadcastConnectOptInSettingKeyForUser(fixture.ctx), "true"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Disconnect(live.sessionID); err != nil {
		t.Fatal(err)
	}

	// Unknown host key on the dropped target is rejected without dialing.
	unknown := fixture.openTarget(t, "Unknown API", "api.unknown.example", "")
	if err := fixture.manager.Disconnect(unknown.sessionID); err != nil {
		t.Fatal(err)
	}
	response := fixture.prepare([]string{unknown.tabID}, "hostname\n")
	if response.OK || response.Error == nil {
		t.Fatalf("unknown host key resolve = %+v", response)
	}

	// Production assets need the broadcast production confirmation.
	if err := fixture.manager.Disconnect(fixture.production.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.production.example", 22, "ssh-ed25519", "SHA256:production"); err != nil {
		t.Fatal(err)
	}
	before := fixture.connector.dials.Load()
	response = fixture.prepare([]string{fixture.production.tabID}, "hostname\n")
	if response.OK || response.Error == nil {
		t.Fatalf("unconfirmed production resolve = %+v", response)
	}
	if fixture.connector.dials.Load() != before {
		t.Fatalf("unconfirmed production resolve dialed %d times", fixture.connector.dials.Load()-before)
	}
	if err := fixture.database.SettingSet(fixture.ctx, BroadcastConnectProductionOptInSettingKeyForUser(fixture.ctx), "true"); err != nil {
		t.Fatal(err)
	}
	confirmed := requireBroadcastConfirmation(t, fixture.prepare([]string{fixture.production.tabID}, "hostname\n"))
	requireBroadcastOK(t, fixture.write(fixture.production.tabID, "hostname\n", &terminalBroadcastWriteOptions{
		TargetTabIDs: []string{fixture.production.tabID},
		ProductionConfirmation: &broadcastProductionConfirmation{
			Confirmed: true, TargetSetHash: confirmed.TargetSetHash, InputHash: confirmed.InputHash,
			EnvironmentNames: confirmed.RequiredEnvironmentNames,
		},
	}))
}

func newBroadcastFixtureHostKeys(t *testing.T, fixture *broadcastGateFixture) *productionHostKeyStore {
	t.Helper()
	hostKeys, err := newProductionHostKeyStore(fixture.ctx, filepath.Join(t.TempDir(), "known_hosts.json"), fixture.database, false)
	if err != nil {
		t.Fatal(err)
	}
	return hostKeys
}

func TestBroadcastReconnectFailureDialsOnceWithoutBackgroundRetry(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	fixture.service.hostKeys = newBroadcastFixtureHostKeys(t, fixture)
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.staging.example", 22, "ssh-ed25519", "SHA256:staging"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.SettingSet(fixture.ctx, BroadcastConnectOptInSettingKeyForUser(fixture.ctx), "true"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Disconnect(fixture.staging.sessionID); err != nil {
		t.Fatal(err)
	}
	connectErr := &ssh.ConnectError{Kind: ssh.ErrorKindNetwork, Err: errors.New("temporary failure")}
	fixture.connector.connectErr = func(string, int32) error { return connectErr }
	initialCalls := fixture.connector.dials.Load()

	response := fixture.prepare([]string{fixture.staging.tabID}, "hostname\n")
	if response.OK || response.Error == nil {
		t.Fatalf("retryable failure resolve = %+v, want the dial error", response)
	}
	if !strings.Contains(response.Error.Message, "网络连接失败") || strings.Contains(response.Error.Message, "内部错误") {
		t.Fatalf("resolve error = %+v, want the actual dial error propagated", response.Error)
	}
	if got := fixture.connector.dials.Load(); got != initialCalls+1 {
		t.Fatalf("reconnect dialed %d times, want exactly one (%d)", got-initialCalls, initialCalls+1)
	}
	// The retryable failure must not schedule any background retry.
	time.Sleep(100 * time.Millisecond)
	if got := fixture.connector.dials.Load(); got != initialCalls+1 {
		t.Fatalf("background dial after return: %d calls", got-initialCalls)
	}
}

func TestBroadcastConnectGateIPCErrorClassification(t *testing.T) {
	fixture := newBroadcastGateFixture(t)
	fixture.service.hostKeys = newBroadcastFixtureHostKeys(t, fixture)
	if err := fixture.database.SettingSet(fixture.ctx, BroadcastConnectOptInSettingKeyForUser(fixture.ctx), "true"); err != nil {
		t.Fatal(err)
	}

	unknown := fixture.openTarget(t, "Unknown API", "api.unknown.example", "")
	if err := fixture.manager.Disconnect(unknown.sessionID); err != nil {
		t.Fatal(err)
	}
	response := fixture.prepare([]string{unknown.tabID}, "hostname\n")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeHostKeyPending ||
		!strings.Contains(response.Error.Message, guard.ErrConnectHostKeyUnknown.Error()) {
		t.Fatalf("unknown host key response = %+v", response)
	}

	credentialID, err := fixture.database.CredentialPut(fixture.ctx, store.CredentialInput{
		Name: "key", Kind: vault.KindPrivateKey, Nonce: []byte{1}, Blob: []byte{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	locked := fixture.openTarget(t, "Locked API", "api.locked.example", "")
	if _, err := fixture.database.AssetUpdate(fixture.ctx, locked.assetID, store.AssetPatch{CredID: store.Value(credentialID)}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.locked.example", 22, "ssh-ed25519", "SHA256:locked"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Disconnect(locked.sessionID); err != nil {
		t.Fatal(err)
	}
	response = fixture.prepare([]string{locked.tabID}, "hostname\n")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeVaultLocked ||
		!strings.Contains(response.Error.Message, guard.ErrConnectVaultUnavailable.Error()) {
		t.Fatalf("vault unavailable response = %+v", response)
	}

	production := fixture.openTarget(t, "Production API", "api.production2.example", "env:production")
	if err := fixture.database.KnownHostAccept(fixture.ctx, "api.production2.example", 22, "ssh-ed25519", "SHA256:production2"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Disconnect(production.sessionID); err != nil {
		t.Fatal(err)
	}
	response = fixture.prepare([]string{production.tabID}, "hostname\n")
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm ||
		!strings.Contains(response.Error.Message, guard.ErrConnectProductionConfirmationRequired.Error()) {
		t.Fatalf("production unconfirmed response = %+v", response)
	}

	other := ipc.WithUserID(t.Context(), "broadcast-other")
	response = dispatchBroadcastTest(fixture.dispatcher, other, "terminal_broadcast_prepare", terminalBroadcastPrepareRequest{
		TabIDs: []string{fixture.staging.tabID}, Data: broadcastTestInts("hostname\n"),
	})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("cross-owner response = %+v", response)
	}
}
