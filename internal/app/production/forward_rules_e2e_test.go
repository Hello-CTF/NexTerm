//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newSavedRuleDispatcherProduction(t *testing.T, dataDir, upstream string) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		DataDir: dataDir,
		Desktop: false,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return &fakeDialTransport{
				fakeSSHTransport: fakeSSHTransport{kind: asset.Kind, generation: generation},
				upstream:         upstream,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		_ = production.Shutdown(context.Background())
		t.Fatal(err)
	}
	return production
}

func dispatchSavedRuleProduction(t *testing.T, production *Production, ctx context.Context, command string, args any) ipc.Response {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return production.Dispatcher.Dispatch(ctx, ipc.Request{
		Command: command, Args: encoded, ClientID: "saved-rule-e2e",
	}, production.Environment("saved-rule-e2e"))
}

func requireSavedRuleProductionResponse[T any](t *testing.T, response ipc.Response) T {
	t.Helper()
	if !response.OK {
		t.Fatalf("dispatch failed: %+v", response.Error)
	}
	var result T
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatalf("decode dispatch result: %v", err)
	}
	return result
}

func requireSavedRuleProductionError(t *testing.T, response ipc.Response, code ipc.Code) {
	t.Helper()
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %+v, want error code %s", response, code)
	}
}

func freeSavedRuleProductionPort(t *testing.T) int32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := int32(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func requireSavedRuleProductionPortClosed(t *testing.T, port int32) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatalf("saved-rule port %d is still accepting connections", port)
	}
}

func createSavedRuleProductionAsset(t *testing.T, production *Production, ctx context.Context, name string) string {
	t.Helper()
	response := dispatchSavedRuleProduction(t, production, ctx, "asset_create", map[string]any{
		"args": map[string]any{"kind": "ssh", "name": name, "options": map[string]any{}},
	})
	created := requireSavedRuleProductionResponse[struct {
		ID string `json:"id"`
	}](t, response)
	if created.ID == "" {
		t.Fatal("created asset has no ID")
	}
	return created.ID
}

func TestProductionSavedRuleDispatcherLifecycleOwnerAndReopen(t *testing.T) {
	upstream := startEchoListener(t)
	dataDir := t.TempDir()
	var production *Production
	t.Cleanup(func() {
		if production != nil {
			_ = production.Shutdown(context.Background())
		}
	})
	production = newSavedRuleDispatcherProduction(t, dataDir, upstream)
	if !production.Services.Store.GORM().Migrator().HasTable("forward_rule") {
		t.Fatal("normal production migration did not create forward_rule")
	}
	ownerA := ipc.WithUserID(context.Background(), "saved-rule-owner-a")
	ownerB := ipc.WithUserID(context.Background(), "saved-rule-owner-b")
	assetA := createSavedRuleProductionAsset(t, production, ownerA, "owner-a-ssh")
	assetB := createSavedRuleProductionAsset(t, production, ownerB, "owner-b-ssh")
	connectedA, err := production.Services.Sessions.Connect(ownerA, session.Asset{ID: assetA, Kind: session.KindSSH, Name: "owner-a-ssh"})
	if err != nil {
		t.Fatal(err)
	}
	connectedB, err := production.Services.Sessions.Connect(ownerB, session.Asset{ID: assetB, Kind: session.KindSSH, Name: "owner-b-ssh"})
	if err != nil {
		t.Fatal(err)
	}

	listenPort := freeSavedRuleProductionPort(t)
	saveInput := map[string]any{
		"assetId": assetA, "name": "production-database", "listenPort": listenPort,
		"targetHost": "remote.internal", "targetPort": 22,
	}
	saved := requireSavedRuleProductionResponse[forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_save", saveInput))
	if saved.ID == "" || saved.Status != forward.StatusStopped || saved.ListenHost != "127.0.0.1" {
		t.Fatalf("saved rule = %+v", saved)
	}
	foreignRules := requireSavedRuleProductionResponse[[]forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerB, "forward_rule_list", struct{}{}))
	if len(foreignRules) != 0 {
		t.Fatalf("other owner sees saved rules: %+v", foreignRules)
	}
	requireSavedRuleProductionError(t,
		dispatchSavedRuleProduction(t, production, ownerB, "forward_rule_start", map[string]any{
			"id": saved.ID, "sessionId": connectedB.ID,
		}), ipc.CodeNotFound)
	requireSavedRuleProductionError(t,
		dispatchSavedRuleProduction(t, production, ownerB, "forward_rule_delete", map[string]any{"id": saved.ID}), ipc.CodeNotFound)

	started := requireSavedRuleProductionResponse[forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_start", map[string]any{
			"id": saved.ID, "sessionId": connectedA.ID,
		}))
	if started.ID != saved.ID || started.Status != forward.StatusActive || started.ListenPort != listenPort || started.ListenHost != "127.0.0.1" {
		t.Fatalf("started rule = %+v", started)
	}
	runtimes := requireSavedRuleProductionResponse[[]forward.Spec](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_list", struct{}{}))
	if len(runtimes) != 1 || runtimes[0].ID != saved.ID || runtimes[0].ListenPort != uint16(listenPort) || runtimes[0].ListenHost != "127.0.0.1" {
		t.Fatalf("runtime list = %+v", runtimes)
	}
	foreignRuntimes := requireSavedRuleProductionResponse[[]forward.Spec](t,
		dispatchSavedRuleProduction(t, production, ownerB, "forward_list", struct{}{}))
	if len(foreignRuntimes) != 0 {
		t.Fatalf("other owner sees rule runtime: %+v", foreignRuntimes)
	}
	connection := assertForwardEcho(t, runtimes[0], "production-saved-rule-relay")
	_ = connection.Close()
	listed := requireSavedRuleProductionResponse[[]forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_list", struct{}{}))
	if len(listed) != 1 || listed[0].ID != saved.ID || listed[0].Status != forward.StatusActive {
		t.Fatalf("active saved-rule list = %+v", listed)
	}

	stopped := requireSavedRuleProductionResponse[forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_stop", map[string]any{"id": saved.ID}))
	if stopped.ID != saved.ID || stopped.Status != forward.StatusStopped {
		t.Fatalf("stopped rule = %+v", stopped)
	}
	runtimes = requireSavedRuleProductionResponse[[]forward.Spec](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_list", struct{}{}))
	if len(runtimes) != 0 {
		t.Fatalf("stopped rule retained runtime: %+v", runtimes)
	}
	requireSavedRuleProductionPortClosed(t, listenPort)

	started = requireSavedRuleProductionResponse[forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_start", map[string]any{
			"id": saved.ID, "sessionId": connectedA.ID,
		}))
	if started.Status != forward.StatusActive {
		t.Fatalf("restarted rule = %+v", started)
	}
	if err := production.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	production = nil

	production = newSavedRuleDispatcherProduction(t, dataDir, upstream)
	if !production.Services.Store.GORM().Migrator().HasTable("forward_rule") {
		t.Fatal("reopened production migration lost forward_rule")
	}
	listed = requireSavedRuleProductionResponse[[]forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_list", struct{}{}))
	if len(listed) != 1 || listed[0].ID != saved.ID || listed[0].Status != forward.StatusStopped || listed[0].SessionID != "" {
		t.Fatalf("saved rules after reopen = %+v", listed)
	}
	runtimes = requireSavedRuleProductionResponse[[]forward.Spec](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_list", struct{}{}))
	if len(runtimes) != 0 {
		t.Fatalf("reopen auto-started saved rules: %+v", runtimes)
	}
	requireSavedRuleProductionPortClosed(t, listenPort)
	requireProductionNull(t, dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_delete", map[string]any{"id": saved.ID}))
	listed = requireSavedRuleProductionResponse[[]forward.Rule](t,
		dispatchSavedRuleProduction(t, production, ownerA, "forward_rule_list", struct{}{}))
	if len(listed) != 0 {
		t.Fatalf("deleted rule remains: %+v", listed)
	}
}
