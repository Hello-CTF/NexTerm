package tools

import "testing"

// AI-started batches must never interpret script directives: only a trusted runbook
// run sets an Interactor, so a prompt can never be raised on, or answered for, the model.
func TestAIExecBatchRequestsCarryNoInteractor(t *testing.T) {
	request := execBatchRequest("job", Call{ID: "call"}, ExecCommandsArgs{Commands: []string{"echo @NEXTERM:SECRET token Token"}}, ExecTargetSet{SessionIDs: []string{"s1"}}, "echo @NEXTERM:SECRET token Token", 0)
	if request.Interactor != nil {
		t.Fatal("AI exec batch request must not carry an Interactor")
	}
}
