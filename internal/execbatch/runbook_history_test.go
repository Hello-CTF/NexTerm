package execbatch

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestExecuteHistoryPersistsRunbookStepAssociation(t *testing.T) {
	database := openExecBatchHistoryStore(t)
	resolver := &fakeResolver{targets: map[string]Target{
		"s1": targetFor("s1", successTransport()),
	}}
	service := New(Config{Resolver: resolver, History: database})
	request := Request{
		BatchID: "run-history:s01:g01", SessionIDs: []string{"s1"}, Command: "true",
		Runbook: &RunbookMetadata{
			RunbookID: "runbook-deploy", RunID: "run-history", StepNumber: 1, StepCount: 1,
			GroupNumber: 1, GroupCount: 2,
		},
	}
	if _, err := service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatal(err)
	}
	history, err := database.ExecBatchRunHistoryGet(execBatchHistoryContext("owner-a"), "run-history")
	if err != nil {
		t.Fatal(err)
	}
	if history.RunbookID != "runbook-deploy" || history.ReachedStep != 1 || history.StepCount != 1 || history.Status != store.ExecBatchStatusRunning {
		t.Fatalf("partial group history = %+v", history)
	}
	if len(history.Batches) != 1 || history.Batches[0].Batch.StepNumber != 1 || len(history.Batches[0].Results) != 1 ||
		history.Batches[0].Results[0].SessionID != "s1" {
		t.Fatalf("run batches = %+v", history.Batches)
	}
	request.BatchID = "run-history:s01:g02"
	request.Runbook.GroupNumber = 2
	if _, err := service.Execute(t.Context(), "owner-a", request); err != nil {
		t.Fatal(err)
	}
	history, err = database.ExecBatchRunHistoryGet(execBatchHistoryContext("owner-a"), "run-history")
	if err != nil {
		t.Fatal(err)
	}
	if history.Status != store.ExecBatchStatusSuccess || len(history.Batches) != 2 ||
		history.Batches[0].Batch.GroupNumber != 1 || history.Batches[1].Batch.GroupNumber != 2 {
		t.Fatalf("complete group history = %+v", history)
	}
}
