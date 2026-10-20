package production

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
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
}

func (c *broadcastGateConnector) Connect(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
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
	fixture := &broadcastGateFixture{ctx: ctx, database: database, manager: manager, dispatcher: dispatcher, connector: connector}
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
	}
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
	requireBroadcastOK(t, fixture.write(fixture.staging.tabID, "hostname\n", &terminalBroadcastWriteOptions{TargetTabIDs: tabIDs}))
	if got := fixture.connector.transport(fixture.staging.assetID).firstChannel().written(); got != "hostname\n" {
		t.Fatalf("non-production broadcast input = %q", got)
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
	if count != 0 {
		t.Fatalf("direct paths recorded %d production confirmations", count)
	}
}
