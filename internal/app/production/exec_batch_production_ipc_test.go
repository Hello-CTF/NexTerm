package production

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
