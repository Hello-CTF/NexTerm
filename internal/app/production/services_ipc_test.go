package production

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	hostservices "github.com/Hello-CTF/NexTerm/internal/services"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestServicesIPCProductionGuardBindsConfirmedSystemctlCommand(t *testing.T) {
	var execCalls atomic.Int32
	var executed string
	transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		executed = command
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
	batches := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-production": {ID: "asset-production", Name: "Production API", Host: &hostname, Tags: "env:prod"},
	}), auditor)
	dispatcher := ipc.NewDispatcher()
	if err := registerServiceCommands(dispatcher, batches); err != nil {
		t.Fatal(err)
	}
	request := hostservices.ActionRequest{
		BatchID: "services-production-restart", SessionIDs: []string{connected.ID},
		Unit: "nginx.service", Action: hostservices.ActionRestart, Sudo: true,
	}
	response := dispatchExecBatchTest(dispatcher, owner, "services_action", request)
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("unconfirmed services_action = %+v", response)
	}
	if execCalls.Load() != 0 {
		t.Fatalf("unconfirmed services_action executed %d commands", execCalls.Load())
	}
	encoded, err := json.Marshal(response.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail execbatch.ConfirmationRequiredDetail
	if err := json.Unmarshal(encoded, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.BatchID != request.BatchID || len(detail.ProductionTargets) != 1 || detail.ProductionTargets[0].Environment != "production" {
		t.Fatalf("production detail = %+v", detail)
	}
	request.ProductionConfirmation = &execbatch.ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
	}
	confirmed := dispatchExecBatchTest(dispatcher, owner, "services_action", request)
	batch := decodeExecBatchTest[hostservices.ServiceOperationBatch](t, confirmed)
	if len(batch.Hosts) != 1 || batch.Hosts[0].Result.Status != execbatch.StatusSuccess || execCalls.Load() != 1 {
		t.Fatalf("confirmed services_action = %+v, calls = %d", batch, execCalls.Load())
	}
	want, err := hostservices.SystemdActionCommand(hostservices.ActionRestart, "nginx.service", true)
	if err != nil {
		t.Fatal(err)
	}
	if executed != want || batch.Command != want {
		t.Fatalf("executed command = %q, batch command = %q, want %q", executed, batch.Command, want)
	}
}
