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
	if err := registerServiceCommands(dispatcher, batches, manager); err != nil {
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
		EnvironmentNames: detail.RequiredEnvironmentNames,
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

func TestServicesIPCWindowsHostsRunPowerShellBehindProductionConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asset session.Asset
	}{
		{"winrm", session.Asset{ID: "asset-win", Name: "Production Win", Kind: session.KindWinRM}},
		{"ssh with PowerShell shell", session.Asset{ID: "asset-win", Name: "Production Win", Kind: session.KindSSH, Options: map[string]any{"shell": "powershell.exe"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executed []string
			transport := newExecBatchTestTransport(func(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
				executed = append(executed, command)
				code := 0
				return base.ExecResult{ExitCode: &code, Stdout: "Spooler: Running"}, nil
			})
			manager := newExecBatchTestManager(t, transport)
			owner := ipc.WithUserID(t.Context(), "owner-a")
			connected, err := manager.ConnectFor(owner, tc.asset)
			if err != nil {
				t.Fatal(err)
			}
			hostname := "win.production.example"
			auditor := &execBatchProductionAuditor{}
			batches := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
				"asset-win": {ID: "asset-win", Name: "Production Win", Host: &hostname, Tags: "env:prod"},
			}), auditor)
			dispatcher := ipc.NewDispatcher()
			if err := registerServiceCommands(dispatcher, batches, manager); err != nil {
				t.Fatal(err)
			}
			request := hostservices.ActionRequest{
				BatchID: "services-windows-restart", SessionIDs: []string{connected.ID},
				Unit: "Spooler", Action: hostservices.ActionRestart,
			}
			response := dispatchExecBatchTest(dispatcher, owner, "services_action", request)
			if response.OK || response.Error == nil || response.Error.Code != ipc.CodeNeedsConfirm || len(executed) != 0 {
				t.Fatalf("unconfirmed services_action = %+v, executed = %v", response, executed)
			}
			encoded, _ := json.Marshal(response.Error.Detail)
			var detail execbatch.ConfirmationRequiredDetail
			if err := json.Unmarshal(encoded, &detail); err != nil {
				t.Fatal(err)
			}
			request.ProductionConfirmation = &execbatch.ProductionConfirmation{
				Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
				EnvironmentNames: detail.RequiredEnvironmentNames,
			}
			batch := decodeExecBatchTest[hostservices.ServiceOperationBatch](t, dispatchExecBatchTest(dispatcher, owner, "services_action", request))
			want, err := hostservices.WindowsActionCommand(hostservices.ActionRestart, "Spooler")
			if err != nil {
				t.Fatal(err)
			}
			if len(executed) != 1 || executed[0] != want || batch.Command != want {
				t.Fatalf("executed = %v, batch command = %q", executed, batch.Command)
			}
			if len(batch.Hosts) != 1 || batch.Hosts[0].Platform != hostservices.PlatformWindows || batch.Hosts[0].Result.Status != execbatch.StatusSuccess {
				t.Fatalf("hosts = %+v", batch.Hosts)
			}
			auditor.mu.Lock()
			audited := len(auditor.records)
			auditor.mu.Unlock()
			if audited == 0 {
				t.Fatal("confirmed Windows service action was not audited")
			}
		})
	}
}

func TestServicesIPCWindowsListChecksOwnerAndKeepsGenericBatchesPOSIXOnly(t *testing.T) {
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{ExitCode: &code, Stdout: "@@nexterm-services windows\n[]"}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	winrm, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-win", Name: "win", Kind: session.KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	batches := newExecBatchService(manager, execBatchTestAssets(nil), nil)
	dispatcher := ipc.NewDispatcher()
	if err := registerServiceCommands(dispatcher, batches, manager); err != nil {
		t.Fatal(err)
	}
	if err := (newExecBatchIPCService(manager, execBatchTestAssets(nil), nil)).register(dispatcher); err != nil {
		t.Fatal(err)
	}
	response := dispatchExecBatchTest(dispatcher, ownerB, "services_list", hostservices.ListRequest{BatchID: "other", SessionIDs: []string{winrm.ID}})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeForbidden {
		t.Fatalf("other-owner services_list = %+v", response)
	}
	listed := decodeExecBatchTest[hostservices.ServiceOperationBatch](t, dispatchExecBatchTest(dispatcher, ownerA, "services_list", hostservices.ListRequest{BatchID: "own", SessionIDs: []string{winrm.ID}}))
	if len(listed.Hosts) != 1 || listed.Hosts[0].Platform != hostservices.PlatformWindows || listed.Hosts[0].FailureKind != "" {
		t.Fatalf("services_list = %+v", listed)
	}
	response = dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_execute", execbatch.Request{BatchID: "generic", SessionIDs: []string{winrm.ID}, Command: "hostname"})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("generic batch on WinRM = %+v", response)
	}
}
