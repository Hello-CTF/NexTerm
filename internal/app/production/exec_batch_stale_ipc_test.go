package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestExecBatchIPCErrorMappingStale(t *testing.T) {
	err := execBatchIPCError(fmt.Errorf("validate: %w", execbatch.ErrStaleProductionConfirmation))
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.Code("conflict") {
		t.Fatalf("stale mapping = %+v", err)
	}
	if !strings.Contains(appErr.Message, "stale") || !strings.Contains(appErr.Message, "preflight") {
		t.Fatalf("stale reason is not visible: %+v", appErr)
	}
}

func TestExecBatchIPCStaleConfirmationVisibleAndNonproductionStillRuns(t *testing.T) {
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
	assets := map[string]store.AssetRow{
		"asset-production": {ID: "asset-production", Name: "Production API", Tags: "env:production"},
	}
	service := newExecBatchIPCService(manager, execBatchTestAssets(assets), nil)
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	request := execbatch.Request{BatchID: "batch-stale-ipc", SessionIDs: []string{connected.ID}, Command: "echo safe"}
	challenged := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", request)
	if challenged.OK || challenged.Error == nil || challenged.Error.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("initial challenge = %+v", challenged)
	}
	encodedDetail, err := json.Marshal(challenged.Error.Detail)
	if err != nil {
		t.Fatal(err)
	}
	var detail execbatch.ConfirmationRequiredDetail
	if err := json.Unmarshal(encodedDetail, &detail); err != nil {
		t.Fatal(err)
	}
	assets["asset-production"] = store.AssetRow{ID: "asset-production", Name: "Production API", Tags: "env:staging"}
	request.ProductionConfirmation = &execbatch.ProductionConfirmation{Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash}
	stale := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", request)
	if stale.OK || stale.Error == nil || stale.Error.Code != ipc.Code("conflict") {
		t.Fatalf("stale IPC response = %+v", stale)
	}
	if !strings.Contains(stale.Error.Message, "stale") || !strings.Contains(stale.Error.Message, "preflight") {
		t.Fatalf("stale reason is hidden: %+v", stale.Error)
	}
	if execCalls.Load() != 0 {
		t.Fatalf("stale confirmation executed %d commands", execCalls.Load())
	}

	nonproduction := execbatch.Request{BatchID: "batch-nonproduction-ipc", SessionIDs: []string{connected.ID}, Command: "echo safe"}
	response := dispatchExecBatchTest(dispatcher, owner, "exec_batch_execute", nonproduction)
	batch := decodeExecBatchTest[execbatch.Batch](t, response)
	if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusSuccess || execCalls.Load() != 1 {
		t.Fatalf("nonproduction batch = %+v, calls = %d", batch, execCalls.Load())
	}
}
