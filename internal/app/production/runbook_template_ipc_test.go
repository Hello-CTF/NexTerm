package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/runbook"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestRunbookTemplateIPCStartFromTemplateRecordsVersion(t *testing.T) {
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	other := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-1", Name: "Web", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	batches := newExecBatchService(manager, execBatchTestAssets(map[string]store.AssetRow{
		"asset-1": {ID: "asset-1", Name: "Web"},
	}), &execBatchProductionAuditor{}, database)
	service := newRunbookIPCService(batches, database)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	created := decodeExecBatchTest[store.RunbookTemplate](t, dispatchExecBatchTest(dispatcher, owner, "runbook_template_create", store.RunbookTemplateInput{
		Name:  "Restart web",
		Steps: []store.RunbookTemplateStep{{ID: "s1", Name: "Restart", Command: "echo v1"}},
	}))
	if created.ID == "" || created.Version != 1 || created.UpdatedAt == 0 {
		t.Fatalf("created = %+v", created)
	}
	updateInput := runbookTemplateUpdateRequest{ID: created.ID, RunbookTemplateInput: store.RunbookTemplateInput{
		Name:  "Restart web",
		Steps: []store.RunbookTemplateStep{{ID: "s1", Name: "Restart", Command: "echo v2"}},
	}}
	updated := decodeExecBatchTest[store.RunbookTemplate](t, dispatchExecBatchTest(dispatcher, owner, "runbook_template_update", updateInput))
	if updated.Version != 2 || updated.Steps[0].Command != "echo v2" {
		t.Fatalf("updated = %+v", updated)
	}

	// Another owner cannot see or start the template.
	if resp := dispatchExecBatchTest(dispatcher, other, "runbook_run_start", runbook.StartRequest{
		RunID: "run-x", TemplateID: created.ID, SessionIDs: []string{"s"},
	}); resp.OK || resp.Error == nil || resp.Error.Code != ipc.CodeNotFound {
		t.Fatalf("cross-owner start = %+v", resp)
	}
	// A template reference cannot be combined with inline steps.
	if resp := dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-y", TemplateID: created.ID, RunbookName: "inline", SessionIDs: []string{connected.ID},
	}); resp.OK || resp.Error == nil || resp.Error.Code != ipc.CodeBadParam {
		t.Fatalf("mixed start = %+v", resp)
	}

	state := decodeExecBatchTest[runbook.RunState](t, dispatchExecBatchTest(dispatcher, owner, "runbook_run_start", runbook.StartRequest{
		RunID: "run-tpl", TemplateID: created.ID, SessionIDs: []string{connected.ID},
	}))
	if state.RunbookID != created.ID || state.RunbookVersion != 2 || state.RunbookName != "Restart web" || state.Steps[0].Step.Command != "echo v2" {
		t.Fatalf("start state = %+v", state)
	}
	// Editing the template afterwards must not alter the already started run.
	updateInput.Steps[0].Command = "echo v3"
	dispatchExecBatchTest(dispatcher, owner, "runbook_template_update", updateInput)
	state = decodeExecBatchTest[runbook.RunState](t, dispatchExecBatchTest(dispatcher, owner, "runbook_run_get", runbook.RunRequest{RunID: "run-tpl"}))
	if state.Steps[0].Step.Command != "echo v2" {
		t.Fatalf("run steps changed after template edit: %+v", state.Steps)
	}

	dispatchExecBatchTest(dispatcher, owner, "runbook_run_prepare", runbook.PrepareRequest{
		RunID: "run-tpl", StepNumber: 1,
		Commands: []runbook.RenderedCommand{{SessionID: connected.ID, Command: "echo v2"}},
	})
	state = decodeExecBatchTest[runbook.RunState](t, dispatchExecBatchTest(dispatcher, owner, "runbook_run_advance", runbook.AdvanceRequest{
		RunID: "run-tpl", StepNumber: 1, Confirmed: true,
	}))
	if state.Status != runbook.RunSuccess || state.Steps[0].Results[0].Status != execbatch.StatusSuccess {
		t.Fatalf("advance state = %+v", state)
	}
	history := decodeExecBatchTest[store.ExecBatchRunHistory](t, dispatchExecBatchTest(dispatcher, owner, "runbook_run_history_get", runbook.RunRequest{RunID: "run-tpl"}))
	if history.RunbookID != created.ID || len(history.Batches) != 1 || history.Batches[0].Batch.RunbookVersion != 2 {
		t.Fatalf("history = %+v", history)
	}
}
