package production

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	execBatchHealthPendingCancelTTL          = 10 * time.Second
	execBatchHealthMaxPendingCancels         = 128
	execBatchHealthMaxPendingCancelsPerOwner = 16
)

type execBatchHealthRun struct {
	batchID string
	ownerID string
	cancel  context.CancelFunc
	done    chan struct{}
}

type execBatchHealthCancelKey struct {
	ownerID string
	batchID string
}

type execBatchHealthIPCService struct {
	batches       *execbatch.Service
	probeAssets   func(context.Context, assetProbeBatchRequest) (assetProbeBatchDTO, error)
	latestHistory func(context.Context, []string) ([]store.ExecBatchAssetHistory, error)

	mu             sync.Mutex
	runs           map[execBatchHealthCancelKey]*execBatchHealthRun
	pendingCancels map[execBatchHealthCancelKey]time.Time
}

func newExecBatchHealthIPCService(batches *execbatch.Service, probes *terminalCommandService, history *store.Store) *execBatchHealthIPCService {
	service := &execBatchHealthIPCService{
		batches: batches, runs: make(map[execBatchHealthCancelKey]*execBatchHealthRun),
		pendingCancels: make(map[execBatchHealthCancelKey]time.Time),
	}
	if probes != nil {
		service.probeAssets = probes.probeAssetBatch
	}
	if history != nil {
		service.latestHistory = history.ExecBatchHistoryLatestByAssets
	}
	return service
}

func registerExecBatchHealthCommands(dispatcher *ipc.Dispatcher, batches *execbatch.Service, probes *terminalCommandService, history *store.Store) error {
	return newExecBatchHealthIPCService(batches, probes, history).register(dispatcher)
}

func (s *execBatchHealthIPCService) register(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "exec_batch_health_probe", s.probe); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "exec_batch_health_cancel", s.cancel)
}

func (s *execBatchHealthIPCService) probe(ctx context.Context, _ *ipc.Call, input execbatch.ExecBatchHealthRequest) (execbatch.ExecBatchHealth, error) {
	if s.batches == nil {
		return execbatch.ExecBatchHealth{}, ipc.NewError(ipc.CodeUnsupported, "主机健康探测不可用")
	}
	ctx = productionSessionRequestContext(ctx)
	if input.BatchID == "" || len(input.BatchID) > 128 || strings.TrimSpace(input.BatchID) != input.BatchID {
		return execbatch.ExecBatchHealth{}, execBatchIPCError(fmt.Errorf("%w: invalid health batchId", execbatch.ErrInvalid))
	}
	runCtx, cancel := context.WithCancel(ctx)
	run, err := s.startRun(ipc.OwnerID(ctx), input.BatchID, cancel)
	if err != nil {
		cancel()
		return execbatch.ExecBatchHealth{}, execBatchIPCError(err)
	}
	defer s.finishRun(run)
	batch, err := s.batches.ProbeHealth(runCtx, ipc.OwnerID(ctx), input)
	if err != nil {
		return execbatch.ExecBatchHealth{}, execBatchIPCError(err)
	}
	if runCtx.Err() != nil {
		return batch, nil
	}
	s.probeDisconnected(runCtx, input, &batch)
	if runCtx.Err() != nil {
		return batch, nil
	}
	s.attachHistory(runCtx, input, &batch)
	return batch, nil
}

func (s *execBatchHealthIPCService) startRun(ownerID, batchID string, cancel context.CancelFunc) (*execBatchHealthRun, error) {
	run := &execBatchHealthRun{batchID: batchID, ownerID: ownerID, cancel: cancel, done: make(chan struct{})}
	key := execBatchHealthCancelKey{ownerID: ownerID, batchID: batchID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgePendingCancelsLocked(time.Now())
	if _, exists := s.runs[key]; exists {
		return nil, fmt.Errorf("%w: %s", execbatch.ErrBatchConflict, batchID)
	}
	s.runs[key] = run
	if _, exists := s.pendingCancels[key]; exists {
		delete(s.pendingCancels, key)
		cancel()
	}
	return run, nil
}

func (s *execBatchHealthIPCService) finishRun(run *execBatchHealthRun) {
	key := execBatchHealthCancelKey{ownerID: run.ownerID, batchID: run.batchID}
	s.mu.Lock()
	if s.runs[key] == run {
		delete(s.runs, key)
	}
	s.mu.Unlock()
	run.cancel()
	close(run.done)
}

func (s *execBatchHealthIPCService) purgePendingCancelsLocked(now time.Time) {
	for key, expiresAt := range s.pendingCancels {
		if !expiresAt.After(now) {
			delete(s.pendingCancels, key)
		}
	}
}

func (s *execBatchHealthIPCService) recordPendingCancelLocked(key execBatchHealthCancelKey) error {
	if _, exists := s.pendingCancels[key]; exists {
		return nil
	}
	ownerCount := 0
	for candidate := range s.pendingCancels {
		if candidate.ownerID == key.ownerID {
			ownerCount++
		}
	}
	if len(s.pendingCancels) >= execBatchHealthMaxPendingCancels || ownerCount >= execBatchHealthMaxPendingCancelsPerOwner {
		return fmt.Errorf("%w: %s", execbatch.ErrPendingCancelLimit, key.batchID)
	}
	s.pendingCancels[key] = time.Now().Add(execBatchHealthPendingCancelTTL)
	return nil
}

func (s *execBatchHealthIPCService) cancel(ctx context.Context, _ *ipc.Call, input execBatchCancelRequest) (execbatch.CancelResult, error) {
	if s.batches == nil {
		return execbatch.CancelResult{}, ipc.NewError(ipc.CodeUnsupported, "主机健康探测不可用")
	}
	if strings.TrimSpace(input.BatchID) == "" {
		return execbatch.CancelResult{}, execBatchIPCError(fmt.Errorf("%w: batchId is required", execbatch.ErrInvalid))
	}
	ownerID := ipc.OwnerID(ctx)
	key := execBatchHealthCancelKey{ownerID: ownerID, batchID: input.BatchID}
	s.mu.Lock()
	s.purgePendingCancelsLocked(time.Now())
	run := s.runs[key]
	if run == nil {
		if err := s.recordPendingCancelLocked(key); err != nil {
			s.mu.Unlock()
			return execbatch.CancelResult{}, execBatchIPCError(err)
		}
	}
	s.mu.Unlock()
	if run != nil {
		run.cancel()
	}
	result, err := s.batches.Cancel(ctx, ownerID, input.BatchID)
	if err != nil {
		if run == nil {
			s.mu.Lock()
			delete(s.pendingCancels, key)
			s.mu.Unlock()
		}
		return execbatch.CancelResult{}, execBatchIPCError(err)
	}
	if run != nil {
		select {
		case <-run.done:
		case <-ctx.Done():
			return execbatch.CancelResult{}, execBatchIPCError(ctx.Err())
		}
	}
	return result, nil
}

func (s *execBatchHealthIPCService) probeDisconnected(ctx context.Context, input execbatch.ExecBatchHealthRequest, batch *execbatch.ExecBatchHealth) {
	indexes := make([]int, 0, len(batch.Results))
	assetIDs := make([]string, 0, len(batch.Results))
	for index := range batch.Results {
		result := &batch.Results[index]
		if result.SessionID != "" {
			continue
		}
		result.Error = "未连接 SSH，无法采集系统指标"
		indexes = append(indexes, index)
		assetIDs = append(assetIDs, result.AssetID)
	}
	if len(indexes) == 0 {
		return
	}
	if s.probeAssets == nil {
		for _, index := range indexes {
			batch.Results[index].Error = "未连接 SSH，且资产 reachability 探测不可用"
		}
		return
	}
	timeoutMS := input.TimeoutMS
	if timeoutMS <= 0 || timeoutMS > 5000 {
		timeoutMS = 5000
	}
	probed, err := s.probeAssets(ctx, assetProbeBatchRequest{
		AssetIDs: assetIDs, TimeoutMS: timeoutMS, MaxConcurrent: input.MaxConcurrent,
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		for _, index := range indexes {
			batch.Results[index].Error = fmt.Sprintf("资产 reachability 探测失败：%v", err)
		}
		return
	}
	byAsset := make(map[string]assetProbeResultDTO, len(probed.Results))
	for _, result := range probed.Results {
		byAsset[result.AssetID] = result
	}
	for _, index := range indexes {
		result := &batch.Results[index]
		probe, exists := byAsset[result.AssetID]
		if !exists {
			result.Error = "资产 reachability 探测未返回结果"
			continue
		}
		result.Reachable = probe.Reachable
		result.LatencyMS = probe.DurationMS
		if !probe.Reachable {
			result.Error = probe.Error
			if result.Error == "" {
				result.Error = "主机不可达"
			}
		}
	}
}

func (s *execBatchHealthIPCService) attachHistory(ctx context.Context, input execbatch.ExecBatchHealthRequest, batch *execbatch.ExecBatchHealth) {
	if s.latestHistory == nil {
		batch.HistoryError = "批量执行历史不可用"
		return
	}
	assetIDs := make([]string, len(input.Targets))
	for index, target := range input.Targets {
		assetIDs[index] = target.AssetID
	}
	rows, err := s.latestHistory(ctx, assetIDs)
	if ctx.Err() != nil {
		batch.HistoryError = ctx.Err().Error()
		return
	}
	if err != nil {
		batch.HistoryError = err.Error()
		return
	}
	byAsset := make(map[string]store.ExecBatchAssetHistory, len(rows))
	for _, row := range rows {
		byAsset[row.AssetID] = row
	}
	for index := range batch.Results {
		row, exists := byAsset[batch.Results[index].AssetID]
		if !exists {
			continue
		}
		batch.Results[index].LastBatch = &execbatch.ExecBatchLastBatch{
			BatchID: row.BatchID, Status: row.Status, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		}
	}
}
