package forward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type savedRuleFixture struct {
	db        *store.Store
	manager   *session.Manager
	service   *Service
	ctx       context.Context
	asset     store.AssetRow
	connected *session.Session
	transport *sessionDialTransport
}

func newSavedRuleService(t *testing.T, db *store.Store, manager *session.Manager) *Service {
	t.Helper()
	service := NewService(Config{
		Provider: manager,
		SessionHost: func(ctx context.Context, sessionID string) (string, error) {
			connected, err := manager.SessionFor(ctx, sessionID)
			if err != nil {
				return "", err
			}
			return connected.Asset().ID, nil
		},
		Rules:            db,
		Policy:           Policy{},
		Reconnect:        ReconnectPolicy{Attempts: 1, Backoff: time.Millisecond},
		DialTimeout:      time.Second,
		HandshakeTimeout: time.Second,
		CloseTimeout:     time.Second,
	})
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func newSavedRuleFixture(t *testing.T) *savedRuleFixture {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.GORM().WithContext(t.Context()).AutoMigrate(&store.ForwardRuleRow{}); err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "saved-rule-owner")
	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: "saved-rule-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	transport := newSessionDialTransport(startEchoServer(t), 1)
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		IdleTimeout: -1,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(ctx, session.Asset{ID: asset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := newSavedRuleService(t, db, manager)
	return &savedRuleFixture{db: db, manager: manager, service: service, ctx: ctx, asset: asset, connected: connected, transport: transport}
}

func freeSavedRulePort(t *testing.T) int32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return int32(port)
}

func requireSavedRuleCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func (f *savedRuleFixture) saveRule(t *testing.T, listenPort int32) Rule {
	t.Helper()
	rule, err := f.service.SaveRule(f.ctx, RuleInput{
		AssetID: f.asset.ID, Name: "database", ListenPort: listenPort,
		TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func TestSavedRuleConcurrentStartUsesStableIDAndOneLoopbackListener(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	var listeners atomic.Int32
	fixture.service.listen = func(network, address string) (net.Listener, error) {
		listeners.Add(1)
		want := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rule.ListenPort)))
		if address != want {
			t.Errorf("listen address = %q, want %q", address, want)
		}
		return net.Listen(network, address)
	}

	const starts = 8
	start := make(chan struct{})
	errs := make(chan error, starts)
	var wg sync.WaitGroup
	for range starts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			started, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID)
			if err == nil && (started.ID != rule.ID || started.Status != StatusActive || started.ListenHost != "127.0.0.1") {
				err = fmt.Errorf("unexpected started rule: %+v", started)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := listeners.Load(); got != 1 {
		t.Fatalf("listener creations = %d, want 1", got)
	}
	listed := fixture.service.ListFor(fixture.ctx)
	if len(listed) != 1 || listed[0].ID != rule.ID || listed[0].ListenPort != uint16(rule.ListenPort) || listed[0].ListenHost != "127.0.0.1" {
		t.Fatalf("runtime list = %+v", listed)
	}
	assertEcho(t, dialForward(t, listed[0]), "saved-rule")
	if calls := fixture.transport.calls(); len(calls) != 1 || calls[0] != "remote-only.invalid:4567" {
		t.Fatalf("SSH dial addresses = %v", calls)
	}
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatalf("idempotent start: %v", err)
	}
	if got := listeners.Load(); got != 1 {
		t.Fatalf("listener creations after repeated start = %d", got)
	}
}

func TestSavedRuleSurvivesServiceRebuildWithoutAutoStart(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	if err := fixture.service.Close(); err != nil {
		t.Fatal(err)
	}
	rebuilt := newSavedRuleService(t, fixture.db, fixture.manager)
	if err := rebuilt.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	rules, err := rebuilt.RuleList(fixture.ctx)
	if err != nil || len(rules) != 1 || rules[0].ID != rule.ID || rules[0].Status != StatusStopped {
		t.Fatalf("rules after service rebuild = %+v, %v", rules, err)
	}
	if runtimes := rebuilt.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("service rebuild auto-started rules: %+v", runtimes)
	}
}

func TestSavedRuleRejectsWrongSessionDeletedAssetAndPortConflict(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	otherAsset, err := fixture.db.AssetCreate(fixture.ctx, store.AssetInput{Kind: "ssh", Name: "other-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := fixture.manager.Connect(fixture.ctx, session.Asset{ID: otherAsset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.StartRule(fixture.ctx, rule.ID, otherSession.ID)
	requireSavedRuleCode(t, err, ipc.CodeBadParam)

	foreignCtx := ipc.WithUserID(context.Background(), "other-rule-owner")
	foreignAsset, err := fixture.db.AssetCreate(foreignCtx, store.AssetInput{Kind: "ssh", Name: "foreign-host", OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	foreignSession, err := fixture.manager.Connect(foreignCtx, session.Asset{ID: foreignAsset.ID, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.StartRule(fixture.ctx, rule.ID, foreignSession.ID)
	requireSavedRuleCode(t, err, ipc.CodeForbidden)
	_, err = fixture.service.StartRule(foreignCtx, rule.ID, foreignSession.ID)
	requireSavedRuleCode(t, err, ipc.CodeNotFound)

	occupied, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rule.ListenPort))))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID)
	requireSavedRuleCode(t, err, ipc.CodeIO)
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("port conflict left a runtime: %+v", runtimes)
	}
	if err := fixture.db.AssetDelete(fixture.ctx, fixture.asset.ID); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID)
	requireSavedRuleCode(t, err, ipc.CodeBadParam)
	if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("failed starts removed the definition: %v", err)
	}
}

func TestSavedRuleEditRequiresStoppedRuntime(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))
	if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
		t.Fatal(err)
	}
	edit := RuleInput{
		ID: rule.ID, AssetID: fixture.asset.ID, Name: "edited", ListenPort: rule.ListenPort,
		TargetHost: "changed.internal", TargetPort: 4567,
	}
	_, err := fixture.service.SaveRule(fixture.ctx, edit)
	requireSavedRuleCode(t, err, ipc.CodeBadParam)
	spacedEdit := edit
	spacedEdit.ID = "  " + rule.ID + "  "
	_, err = fixture.service.SaveRule(fixture.ctx, spacedEdit)
	requireSavedRuleCode(t, err, ipc.CodeBadParam)
	stopped, err := fixture.service.StopRule(fixture.ctx, rule.ID)
	if err != nil || stopped.Status != StatusStopped {
		t.Fatalf("stop rule = %+v, %v", stopped, err)
	}
	if _, err := fixture.service.StopRule(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	edited, err := fixture.service.SaveRule(fixture.ctx, spacedEdit)
	if err != nil || edited.ID != rule.ID || edited.Name != "edited" || edited.TargetHost != "changed.internal" {
		t.Fatalf("edit after stop = %+v, %v", edited, err)
	}
	if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
		t.Fatalf("stopped rule retained runtime: %+v", runtimes)
	}
}

type observingRuleStore struct {
	RuleStore
	beforeDelete func()
	deletes      int
}

func (s *observingRuleStore) ForwardRuleDelete(ctx context.Context, id string) error {
	if s.beforeDelete != nil {
		s.beforeDelete()
	}
	s.deletes++
	return s.RuleStore.ForwardRuleDelete(ctx, id)
}

type failingCloseListener struct {
	net.Listener
	err error
}

func (l *failingCloseListener) Close() error {
	return errors.Join(l.Listener.Close(), l.err)
}

func TestSavedRuleDeleteStopsFirstAndStopFailureRetainsDefinition(t *testing.T) {
	t.Run("delete stops before deleting", func(t *testing.T) {
		fixture := newSavedRuleFixture(t)
		observer := &observingRuleStore{RuleStore: fixture.db}
		fixture.service.rules = observer
		rule := fixture.saveRule(t, freeSavedRulePort(t))
		if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
			t.Fatal(err)
		}
		observer.beforeDelete = func() {
			if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 0 {
				t.Errorf("definition deleted while runtime still present: %+v", runtimes)
			}
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rule.ListenPort))), 50*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				t.Error("definition deleted before listener stopped")
			}
		}
		if err := fixture.service.DeleteRule(fixture.ctx, rule.ID); err != nil {
			t.Fatal(err)
		}
		if observer.deletes != 1 {
			t.Fatalf("delete calls = %d", observer.deletes)
		}
		_, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID)
		requireSavedRuleCode(t, err, ipc.CodeNotFound)
	})

	t.Run("close failure retains definition and mapping", func(t *testing.T) {
		fixture := newSavedRuleFixture(t)
		observer := &observingRuleStore{RuleStore: fixture.db}
		fixture.service.rules = observer
		rule := fixture.saveRule(t, freeSavedRulePort(t))
		fixture.service.listen = func(network, address string) (net.Listener, error) {
			listener, err := net.Listen(network, address)
			if err != nil {
				return nil, err
			}
			return &failingCloseListener{Listener: listener, err: errors.New("close rejected")}, nil
		}
		if _, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.service.StopRule(fixture.ctx, rule.ID)
		requireSavedRuleCode(t, err, ipc.CodeIO)
		if err := fixture.service.DeleteRule(fixture.ctx, rule.ID); err == nil {
			t.Fatal("delete succeeded after listener close failure")
		}
		if observer.deletes != 0 {
			t.Fatalf("store delete calls = %d, want 0", observer.deletes)
		}
		if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
			t.Fatalf("definition lost after close failure: %v", err)
		}
		if runtimes := fixture.service.ListFor(fixture.ctx); len(runtimes) != 1 || runtimes[0].ID != rule.ID {
			t.Fatalf("runtime mapping lost after close failure: %+v", runtimes)
		}
	})
}

func TestSavedRuleIPCLifecycle(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	dispatcher := ipc.NewDispatcher()
	if err := fixture.service.RegisterRuleCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command string, args any) ipc.Response {
		t.Helper()
		encoded, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return dispatcher.Dispatch(fixture.ctx, ipc.Request{Command: command, Args: encoded}, ipc.Environment{})
	}
	port := freeSavedRulePort(t)
	response := dispatch("forward_rule_save", RuleInput{
		AssetID: fixture.asset.ID, Name: "ipc-rule", ListenPort: port,
		TargetHost: "remote-only.invalid", TargetPort: 4567,
	})
	if !response.OK {
		t.Fatalf("save response = %+v", response.Error)
	}
	var saved Rule
	if err := json.Unmarshal(response.Data, &saved); err != nil {
		t.Fatal(err)
	}
	response = dispatch("forward_rule_start", startRuleRequest{ID: saved.ID, SessionID: fixture.connected.ID})
	if !response.OK {
		t.Fatalf("start response = %+v", response.Error)
	}
	response = dispatch("forward_rule_stop", ruleRequest{ID: saved.ID})
	if !response.OK {
		t.Fatalf("stop response = %+v", response.Error)
	}
	response = dispatch("forward_rule_delete", ruleRequest{ID: saved.ID})
	if !response.OK {
		t.Fatalf("delete response = %+v", response.Error)
	}
	response = dispatch("forward_rule_list", struct{}{})
	if !response.OK {
		t.Fatalf("list response = %+v", response.Error)
	}
	var rules []Rule
	if err := json.Unmarshal(response.Data, &rules); err != nil || len(rules) != 0 {
		t.Fatalf("final rules = %+v, %v", rules, err)
	}
}

func TestStartRuleRejectsExposedRuntimeConfusion(t *testing.T) {
	fixture := newSavedRuleFixture(t)
	rule := fixture.saveRule(t, freeSavedRulePort(t))

	// An exposed one-off listener squatting on the rule ID must not be adopted
	// by StartRule or let the rule escape its loopback-only bind.
	squatter, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(rule.ListenPort))))
	if err != nil {
		t.Fatal(err)
	}
	targetHost := "remote-only.invalid"
	targetPort := uint16(4567)
	if _, err := fixture.service.startWithID(
		fixture.ctx, rule.ID, fixture.connected.ID, fixture.asset.ID, KindLocal,
		squatter, "0.0.0.0", net.JoinHostPort(targetHost, strconv.Itoa(int(targetPort))), &targetHost, &targetPort,
	); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID)
	requireSavedRuleCode(t, err, ipc.CodeBadParam)
	runtimes := fixture.service.ListFor(fixture.ctx)
	if len(runtimes) != 1 || runtimes[0].ListenHost != "0.0.0.0" {
		t.Fatalf("confused runtime changed: %+v", runtimes)
	}
	if _, err := fixture.db.ForwardRuleGet(fixture.ctx, rule.ID); err != nil {
		t.Fatalf("rejected start removed the definition: %v", err)
	}

	stopped, err := fixture.service.StopRule(fixture.ctx, rule.ID)
	if err != nil || stopped.Status != StatusStopped {
		t.Fatalf("stop evicting the squatter = %+v, %v", stopped, err)
	}
	started, err := fixture.service.StartRule(fixture.ctx, rule.ID, fixture.connected.ID)
	if err != nil {
		t.Fatalf("start after eviction: %v", err)
	}
	if started.ListenHost != savedListenHost || started.Status != StatusActive {
		t.Fatalf("started rule = %+v", started)
	}
	assertEcho(t, dialForward(t, fixture.service.ListFor(fixture.ctx)[0]), "evicted")
}
