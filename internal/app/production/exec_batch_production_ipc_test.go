package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type execBatchAssetReaderFunc func(context.Context, string) (store.AssetRow, error)

func (f execBatchAssetReaderFunc) AssetGetLive(ctx context.Context, id string) (store.AssetRow, error) {
	return f(ctx, id)
}

func execBatchTestAssets(rows map[string]store.AssetRow) execBatchAssetReader {
	return execBatchAssetReaderFunc(func(_ context.Context, id string) (store.AssetRow, error) {
		if row, exists := rows[id]; exists {
			return row, nil
		}
		return store.AssetRow{ID: id}, nil
	})
}

type execBatchProductionAuditor struct {
	mu      sync.Mutex
	records []store.AuditInput
}

func (a *execBatchProductionAuditor) AuditInsert(_ context.Context, input store.AuditInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, input)
	return nil
}

func TestExecBatchIPCProductionConfirmationUsesServerAssetMetadata(t *testing.T) {
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-production", Name: "Production API", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	hostname := "api.production.example"
	auditor := &execBatchProductionAuditor{}
	service := newExecBatchIPCService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-production": {ID: "asset-production", Name: "Production API", Host: &hostname, Tags: "env:production"},
	}), auditor)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	request := execbatch.Request{
		BatchID: "batch-ipc-production", SessionIDs: []string{connected.ID}, Command: "deploy --secret=ipc-sensitive",
	}
	response := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("unconfirmed production response = %+v", response)
	}
	encodedError, err := json.Marshal(response.Error)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedError), "ipc-sensitive") || strings.Contains(string(encodedError), "deploy --secret") {
		t.Fatalf("confirmation error contains command plaintext: %s", encodedError)
	}
	encodedDetail, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail execbatch.ConfirmationRequiredDetail
	if err := json.Unmarshal(encodedDetail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Kind != execbatch.ConfirmationRequiredKind || len(detail.ProductionTargets) != 1 ||
		detail.ProductionTargets[0].Hostname != hostname || detail.ProductionTargets[0].Environment != "production" {
		t.Fatalf("confirmation detail = %+v", detail)
	}
	if execCalls.Load() != 0 {
		t.Fatalf("unconfirmed IPC executed %d commands", execCalls.Load())
	}

	request.ProductionConfirmation = &execbatch.ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}
	confirmed := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", request)
	batch := decodeExecBatchTest[execbatch.Batch](t, confirmed)
	if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess || execCalls.Load() != 1 {
		t.Fatalf("confirmed IPC batch = %+v, exec calls = %d", batch, execCalls.Load())
	}
	auditor.mu.Lock()
	defer auditor.mu.Unlock()
	if len(auditor.records) != 2 || auditor.records[0].Kind != "exec_batch_production_confirmation" {
		t.Fatalf("production IPC audit records = %+v", auditor.records)
	}
}

func TestProductionConstructionWiresRunbookAndProductionSettings(t *testing.T) {
	owner := ipc.WithUserID(t.Context(), "owner-a")
	database, err := store.OpenInMemory(owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	production, err := NewProductionWithServices(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, ProductionServices{Store: database, Sessions: manager})
	if err != nil {
		t.Fatal(err)
	}
	if production.Services.Runbook == nil {
		t.Fatal("runbook IPC service is not exposed on ProductionServices")
	}
	if production.Services.ExecBatches == nil {
		t.Fatal("exec batch service was not constructed")
	}

	// The wired exec batch service classifies production through the store-backed
	// per-owner configuration, requires the typed environment name, and executes
	// only with the correct name.
	if err := guard.SetProductionEnvironments(owner, database, []string{"live"}); err != nil {
		t.Fatal(err)
	}
	hostname := "api.live.example"
	asset, err := database.AssetCreate(owner, store.AssetInput{Kind: session.KindSSH, Name: "Live API", Host: &hostname, Tags: "env:live"})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := manager.ConnectFor(owner, session.Asset{ID: asset.ID, Name: asset.Name, Kind: session.KindSSH, StoreBacked: true})
	if err != nil {
		t.Fatal(err)
	}
	request := execbatch.Request{BatchID: "batch-wired-live", SessionIDs: []string{connected.ID}, Command: "hostname"}
	_, err = production.Services.ExecBatches.Execute(owner, "owner-a", request)
	var required *execbatch.ConfirmationRequiredError
	if !errors.As(err, &required) || !errors.Is(err, execbatch.ErrConfirmationRequired) {
		t.Fatalf("wired live execute error = %v, want confirmation required", err)
	}
	detail := required.IPCDetail()
	if len(detail.RequiredEnvironmentNames) != 1 || detail.RequiredEnvironmentNames[0] != "live" {
		t.Fatalf("wired required environment names = %v", detail.RequiredEnvironmentNames)
	}
	if execCalls.Load() != 0 {
		t.Fatalf("unconfirmed wired execute ran %d commands", execCalls.Load())
	}
	request.ProductionConfirmation = &execbatch.ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: []string{"production"},
	}
	if _, err := production.Services.ExecBatches.Execute(owner, "owner-a", request); !errors.Is(err, execbatch.ErrConfirmationRequired) {
		t.Fatalf("wrong typed name error = %v, want confirmation required", err)
	}
	if execCalls.Load() != 0 {
		t.Fatalf("wrong typed name executed %d commands", execCalls.Load())
	}
	request.ProductionConfirmation.EnvironmentNames = []string{"live"}
	batch, err := production.Services.ExecBatches.Execute(owner, "owner-a", request)
	if err != nil {
		t.Fatalf("wired live confirmation failed: %v", err)
	}
	if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess || execCalls.Load() != 1 {
		t.Fatalf("wired live batch = %+v, exec calls = %d", batch.Results, execCalls.Load())
	}
}
