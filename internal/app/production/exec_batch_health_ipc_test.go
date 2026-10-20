package production

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestExecBatchHealthIPCCombinesProbeAndOwnedHistory(t *testing.T) {
	output := "__NEXTERM_HEALTH_V1_SECTION__uptime\n10:00 up 5 min, 1 user, load average: 0, 0, 0\n__NEXTERM_HEALTH_V1_END__uptime:0\n" +
		"__NEXTERM_HEALTH_V1_SECTION__load\n0.1 0.2 0.3 1/10 1\n__NEXTERM_HEALTH_V1_END__load:0\n" +
		"__NEXTERM_HEALTH_V1_SECTION__disk\n/dev/sda 100 50 50 50% /\n__NEXTERM_HEALTH_V1_END__disk:0\n" +
		"__NEXTERM_HEALTH_V1_SECTION__memory\ntotal used free\nMem: 100 25 75\n__NEXTERM_HEALTH_V1_END__memory:0\n" +
		"__NEXTERM_HEALTH_V1_SECTION__docker\nImages 1 0 1GB 100MB\n__NEXTERM_HEALTH_V1_END__docker:0\n"
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 0
		return base.ExecResult{Stdout: output, ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-healthy", Name: "healthy", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := int64(1000)
	finishedAt := int64(2000)
	if err := database.ExecBatchHistorySave(ownerA, store.ExecBatchRow{
		ID: "history-old", CommandHash: strings.Repeat("a", 64), TargetSetHash: strings.Repeat("b", 64),
		Status: store.ExecBatchStatusFailed, TargetCount: 1, StartedAt: startedAt, FinishedAt: &finishedAt,
	}, []store.ExecBatchResultRow{{
		SessionID: "history-session", Position: 0, SessionName: "healthy", AssetID: "asset-healthy",
		Status: "exit_error", ErrorKind: store.ExecBatchErrorRemoteExit, StartedAt: &startedAt, FinishedAt: &finishedAt,
	}}); err != nil {
		t.Fatal(err)
	}
	batches := newExecBatchService(manager, execBatchTestAssets(nil), nil, database)
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchHealthCommands(dispatcher, batches, nil, database); err != nil {
		t.Fatal(err)
	}
	request := execbatch.ExecBatchHealthRequest{
		BatchID: "health-ipc",
		Targets: []execbatch.ExecBatchHealthTarget{
			{AssetID: "asset-healthy", SessionID: connected.ID},
			{AssetID: "asset-disconnected"},
		},
	}

	response := dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_health_probe", request)
	batch := decodeExecBatchTest[execbatch.ExecBatchHealth](t, response)
	if got := execCalls.Load(); got != 1 {
		t.Fatalf("health exec calls = %d", got)
	}
	if len(batch.Results) != 2 || !batch.Results[0].Reachable || batch.Results[0].UptimeSeconds == nil || batch.Results[0].DockerReclaimableBytes == nil {
		t.Fatalf("health results = %+v", batch.Results)
	}
	if batch.Results[0].LastBatch == nil || batch.Results[0].LastBatch.BatchID != "history-old" || batch.Results[0].LastBatch.Status != "exit_error" {
		t.Fatalf("owned history = %+v", batch.Results[0].LastBatch)
	}
	if batch.Results[0].MetricErrors != nil || batch.Results[1].Error == "" || batch.Results[1].LastBatch != nil {
		t.Fatalf("partial health results = %+v", batch.Results)
	}
	history, err := database.ExecBatchHistoryList(ownerA, store.ExecBatchHistoryQuery{})
	if err != nil || history.Total != 1 {
		t.Fatalf("health changed history: page=%+v err=%v", history, err)
	}

	request.BatchID = "health-foreign"
	foreign := decodeExecBatchTest[execbatch.ExecBatchHealth](t, dispatchExecBatchTest(dispatcher, ownerB, "exec_batch_health_probe", request))
	if foreign.Results[0].Error == "" || foreign.Results[0].LastBatch != nil {
		t.Fatalf("foreign owner result = %+v", foreign.Results[0])
	}
	if got := execCalls.Load(); got != 1 {
		t.Fatalf("foreign owner executed health script; calls = %d", got)
	}
}

func TestExecBatchHealthIPCCancelChecksOwnerAndFinishesRun(t *testing.T) {
	started := make(chan context.Context, 1)
	transport := newExecBatchTestTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
		started <- ctx
		<-ctx.Done()
		return base.ExecResult{}, ctx.Err()
	})
	manager := newExecBatchTestManager(t, transport)
	ownerA := ipc.WithUserID(t.Context(), "owner-a")
	ownerB := ipc.WithUserID(t.Context(), "owner-b")
	connected, err := manager.ConnectFor(ownerA, session.Asset{ID: "asset-a", Name: "a", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	batches := newExecBatchService(manager, execBatchTestAssets(nil), nil)
	dispatcher := ipc.NewDispatcher()
	if err := registerExecBatchHealthCommands(dispatcher, batches, nil, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_health_probe", execbatch.ExecBatchHealthRequest{
			BatchID: "health-cancel-ipc", Targets: []execbatch.ExecBatchHealthTarget{{AssetID: "asset-a", SessionID: connected.ID}},
		})
	}()
	var execCtx context.Context
	select {
	case execCtx = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("health execution did not start")
	}

	foreign := dispatchExecBatchTest(dispatcher, ownerB, "exec_batch_health_cancel", execBatchCancelRequest{BatchID: "health-cancel-ipc"})
	if foreign.OK || foreign.Error == nil || foreign.Error.Code != ipc.CodeForbidden {
		t.Fatalf("foreign cancel = %+v", foreign)
	}
	if execCtx.Err() != nil {
		t.Fatal("foreign owner canceled the health execution")
	}
	canceled := dispatchExecBatchTest(dispatcher, ownerA, "exec_batch_health_cancel", execBatchCancelRequest{BatchID: "health-cancel-ipc"})
	result := decodeExecBatchTest[execbatch.CancelResult](t, canceled)
	if !result.Canceled || result.BatchID != "health-cancel-ipc" {
		t.Fatalf("cancel result = %+v", result)
	}
	select {
	case response := <-done:
		batch := decodeExecBatchTest[execbatch.ExecBatchHealth](t, response)
		if len(batch.Results) != 1 || batch.Results[0].Status != execbatch.StatusCanceled {
			t.Fatalf("canceled health batch = %+v", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("health IPC did not finish after cancellation")
	}
}

func TestExecBatchHealthIPCCancelBeforeStartSkipsEveryStage(t *testing.T) {
	var execCalls atomic.Int32
	transport := newExecBatchTestTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
		execCalls.Add(1)
		code := 0
		return base.ExecResult{ExitCode: &code}, nil
	})
	manager := newExecBatchTestManager(t, transport)
	owner := ipc.WithUserID(t.Context(), "owner-a")
	connected, err := manager.ConnectFor(owner, session.Asset{ID: "asset-connected", Name: "connected", Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	service := newExecBatchHealthIPCService(newExecBatchService(manager, execBatchTestAssets(nil), nil), nil, nil)
	var reachabilityCalls atomic.Int32
	var historyCalls atomic.Int32
	service.probeAssets = func(context.Context, assetProbeBatchRequest) (assetProbeBatchDTO, error) {
		reachabilityCalls.Add(1)
		return assetProbeBatchDTO{}, nil
	}
	service.latestHistory = func(context.Context, []string) ([]store.ExecBatchAssetHistory, error) {
		historyCalls.Add(1)
		return nil, nil
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}

	canceled := dispatchExecBatchTest(dispatcher, owner, "exec_batch_health_cancel", execBatchCancelRequest{BatchID: "health-early"})
	if result := decodeExecBatchTest[execbatch.CancelResult](t, canceled); !result.Canceled {
		t.Fatalf("early cancel result = %+v", result)
	}
	response := dispatchExecBatchTest(dispatcher, owner, "exec_batch_health_probe", execbatch.ExecBatchHealthRequest{
		BatchID: "health-early",
		Targets: []execbatch.ExecBatchHealthTarget{
			{AssetID: "asset-connected", SessionID: connected.ID},
			{AssetID: "asset-disconnected"},
		},
	})
	batch := decodeExecBatchTest[execbatch.ExecBatchHealth](t, response)
	if len(batch.Results) != 2 {
		t.Fatalf("early-canceled results = %+v", batch.Results)
	}
	for _, result := range batch.Results {
		if result.Status != execbatch.StatusCanceled {
			t.Fatalf("early-canceled result = %+v", result)
		}
	}
	if got := execCalls.Load(); got != 0 {
		t.Fatalf("early cancel executed %d scripts", got)
	}
	if got := reachabilityCalls.Load(); got != 0 {
		t.Fatalf("early cancel started %d reachability batches", got)
	}
	if got := historyCalls.Load(); got != 0 {
		t.Fatalf("early cancel started %d history reads", got)
	}
}

func TestExecBatchHealthIPCCancelDuringReachabilitySkipsHistory(t *testing.T) {
	owner := ipc.WithUserID(t.Context(), "owner-a")
	service := newExecBatchHealthIPCService(newExecBatchService(nil, nil, nil), nil, nil)
	probeStarted := make(chan context.Context, 1)
	service.probeAssets = func(ctx context.Context, _ assetProbeBatchRequest) (assetProbeBatchDTO, error) {
		probeStarted <- ctx
		<-ctx.Done()
		return assetProbeBatchDTO{}, ctx.Err()
	}
	var historyCalls atomic.Int32
	service.latestHistory = func(context.Context, []string) ([]store.ExecBatchAssetHistory, error) {
		historyCalls.Add(1)
		return nil, nil
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchExecBatchTest(dispatcher, owner, "exec_batch_health_probe", execbatch.ExecBatchHealthRequest{
			BatchID: "health-reachability-cancel", Targets: []execbatch.ExecBatchHealthTarget{{AssetID: "asset-disconnected"}},
		})
	}()
	var probeCtx context.Context
	select {
	case probeCtx = <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("reachability stage did not start")
	}
	cancelDone := make(chan ipc.Response, 1)
	go func() {
		cancelDone <- dispatchExecBatchTest(dispatcher, owner, "exec_batch_health_cancel", execBatchCancelRequest{BatchID: "health-reachability-cancel"})
	}()
	select {
	case response := <-cancelDone:
		if result := decodeExecBatchTest[execbatch.CancelResult](t, response); !result.Canceled {
			t.Fatalf("reachability cancel result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reachability cancel did not finish")
	}
	if probeCtx.Err() == nil {
		t.Fatal("reachability context was not canceled")
	}
	select {
	case response := <-done:
		decodeExecBatchTest[execbatch.ExecBatchHealth](t, response)
	case <-time.After(2 * time.Second):
		t.Fatal("health IPC did not finish after reachability cancellation")
	}
	if got := historyCalls.Load(); got != 0 {
		t.Fatalf("canceled reachability was followed by %d history reads", got)
	}
}

func TestExecBatchHealthIPCCancelDuringHistoryFinishesWithoutAttaching(t *testing.T) {
	owner := ipc.WithUserID(t.Context(), "owner-a")
	service := newExecBatchHealthIPCService(newExecBatchService(nil, nil, nil), nil, nil)
	service.probeAssets = func(context.Context, assetProbeBatchRequest) (assetProbeBatchDTO, error) {
		return assetProbeBatchDTO{Results: []assetProbeResultDTO{{AssetID: "asset-history", Reachable: true, DurationMS: 1}}}, nil
	}
	historyStarted := make(chan context.Context, 1)
	var historyCalls atomic.Int32
	service.latestHistory = func(ctx context.Context, _ []string) ([]store.ExecBatchAssetHistory, error) {
		historyCalls.Add(1)
		historyStarted <- ctx
		<-ctx.Done()
		return []store.ExecBatchAssetHistory{{AssetID: "asset-history", BatchID: "must-not-attach", Status: "success"}}, ctx.Err()
	}
	dispatcher := ipc.NewDispatcher()
	if err := service.register(dispatcher); err != nil {
		t.Fatal(err)
	}
	done := make(chan ipc.Response, 1)
	go func() {
		done <- dispatchExecBatchTest(dispatcher, owner, "exec_batch_health_probe", execbatch.ExecBatchHealthRequest{
			BatchID: "health-history-cancel", Targets: []execbatch.ExecBatchHealthTarget{{AssetID: "asset-history"}},
		})
	}()
	var historyCtx context.Context
	select {
	case historyCtx = <-historyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("history stage did not start")
	}
	cancelDone := make(chan ipc.Response, 1)
	go func() {
		cancelDone <- dispatchExecBatchTest(dispatcher, owner, "exec_batch_health_cancel", execBatchCancelRequest{BatchID: "health-history-cancel"})
	}()
	select {
	case response := <-cancelDone:
		if result := decodeExecBatchTest[execbatch.CancelResult](t, response); !result.Canceled {
			t.Fatalf("history cancel result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("history cancel did not finish")
	}
	if historyCtx.Err() == nil {
		t.Fatal("history context was not canceled")
	}
	select {
	case response := <-done:
		batch := decodeExecBatchTest[execbatch.ExecBatchHealth](t, response)
		if len(batch.Results) != 1 || batch.Results[0].LastBatch != nil {
			t.Fatalf("canceled history attached a result: %+v", batch.Results)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("health IPC did not finish after history cancellation")
	}
	if got := historyCalls.Load(); got != 1 {
		t.Fatalf("history calls = %d, want only the canceled read", got)
	}
}
