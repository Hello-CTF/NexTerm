package execbatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const healthProbeCommand = "LC_ALL=C; export LC_ALL; printf '__NEXTERM_HEALTH_V1_SECTION__uptime\\n'; uptime 2>&1; printf '__NEXTERM_HEALTH_V1_END__uptime:%s\\n' \"$?\"; printf '__NEXTERM_HEALTH_V1_SECTION__load\\n'; cat /proc/loadavg 2>&1; printf '__NEXTERM_HEALTH_V1_END__load:%s\\n' \"$?\"; printf '__NEXTERM_HEALTH_V1_SECTION__disk\\n'; df -P / 2>&1; printf '__NEXTERM_HEALTH_V1_END__disk:%s\\n' \"$?\"; printf '__NEXTERM_HEALTH_V1_SECTION__memory\\n'; free -b 2>&1; printf '__NEXTERM_HEALTH_V1_END__memory:%s\\n' \"$?\"; printf '__NEXTERM_HEALTH_V1_SECTION__docker\\n'; docker system df 2>&1; printf '__NEXTERM_HEALTH_V1_END__docker:%s\\n' \"$?\""

type ExecBatchHealthTarget struct {
	AssetID   string `json:"assetId"`
	SessionID string `json:"sessionId,omitempty"`
}

type ExecBatchHealthRequest struct {
	BatchID       string                  `json:"batchId"`
	Targets       []ExecBatchHealthTarget `json:"targets"`
	TimeoutMS     int64                   `json:"timeoutMs,omitempty"`
	MaxConcurrent int                     `json:"maxConcurrent,omitempty"`
}

type ExecBatchHealthMetricError struct {
	Metric string `json:"metric"`
	Error  string `json:"error"`
}

type ExecBatchLastBatch struct {
	BatchID    string `json:"batchId"`
	Status     string `json:"status"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt *int64 `json:"finishedAt"`
}

type ExecBatchHealthResult struct {
	AssetID                string                       `json:"assetId"`
	SessionID              string                       `json:"sessionId,omitempty"`
	Reachable              bool                         `json:"reachable"`
	LatencyMS              int64                        `json:"latencyMs"`
	Status                 Status                       `json:"status"`
	Error                  string                       `json:"error,omitempty"`
	UptimeSeconds          *int64                       `json:"uptimeSeconds,omitempty"`
	Load1                  *float64                     `json:"load1,omitempty"`
	Load5                  *float64                     `json:"load5,omitempty"`
	Load15                 *float64                     `json:"load15,omitempty"`
	DiskUsedPercent        *float64                     `json:"diskUsedPercent,omitempty"`
	MemoryUsedPercent      *float64                     `json:"memoryUsedPercent,omitempty"`
	DockerReclaimableBytes *int64                       `json:"dockerReclaimableBytes,omitempty"`
	MetricErrors           []ExecBatchHealthMetricError `json:"metricErrors,omitempty"`
	LastBatch              *ExecBatchLastBatch          `json:"lastBatch,omitempty"`
}

type ExecBatchHealth struct {
	BatchID      string                  `json:"batchId"`
	Results      []ExecBatchHealthResult `json:"results"`
	HistoryError string                  `json:"historyError,omitempty"`
}

func (s *Service) ProbeHealth(ctx context.Context, ownerID string, request ExecBatchHealthRequest) (ExecBatchHealth, error) {
	timeout, concurrency, err := validateHealthRequest(request)
	if err != nil {
		return ExecBatchHealth{}, err
	}
	needsResolver := false
	for _, target := range request.Targets {
		if target.SessionID != "" {
			needsResolver = true
			break
		}
	}
	if needsResolver && s.resolver == nil {
		return ExecBatchHealth{}, fmt.Errorf("%w: no session resolver", ErrUnsupported)
	}

	runCtx, cancel := context.WithCancel(ctx)
	active := &activeBatch{ownerID: ownerID, cancel: cancel}
	key := batchKey{ownerID: ownerID, batchID: request.BatchID}
	pendingKey := pendingCancelKey{ownerID: ownerID, batchID: request.BatchID}
	err = admit(ctx, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.purgePendingCancelsLocked(s.now())
		if _, exists := s.batches[key]; exists {
			return fmt.Errorf("%w: %s", ErrBatchConflict, request.BatchID)
		}
		s.batches[key] = active
		if _, exists := s.pendingCancels[pendingKey]; exists {
			delete(s.pendingCancels, pendingKey)
			active.canceling = true
			cancel()
		}
		return nil
	})
	if err != nil {
		cancel()
		return ExecBatchHealth{}, err
	}
	defer func() {
		s.mu.Lock()
		if s.batches[key] == active {
			delete(s.batches, key)
		}
		s.mu.Unlock()
		cancel()
	}()

	results := make([]ExecBatchHealthResult, len(request.Targets))
	jobs := make(chan int, len(request.Targets))
	for index := range request.Targets {
		jobs <- index
	}
	close(jobs)
	if concurrency > len(request.Targets) {
		concurrency = len(request.Targets)
	}
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				target := request.Targets[index]
				results[index] = ExecBatchHealthResult{
					AssetID: target.AssetID, SessionID: target.SessionID,
					Status: StatusCanceled, Error: context.Canceled.Error(),
				}
				if !s.beginExecution(runCtx, active) {
					continue
				}
				results[index] = func() ExecBatchHealthResult {
					defer active.executions.Done()
					return s.probeHealthTarget(runCtx, request.BatchID, target, timeout)
				}()
			}
		}()
	}
	workers.Wait()
	return ExecBatchHealth{BatchID: request.BatchID, Results: results}, nil
}

func (s *Service) probeHealthTarget(ctx context.Context, batchID string, target ExecBatchHealthTarget, timeout time.Duration) ExecBatchHealthResult {
	result := ExecBatchHealthResult{AssetID: target.AssetID, SessionID: target.SessionID, Status: StatusTransportError}
	if target.SessionID == "" {
		result.Error = ErrDisconnected.Error()
		return result
	}
	// The probe runs one fixed read-only command, so managed devices are probed without the
	// typed production confirmation that Execute requires.
	resolved, err := s.resolver.Resolve(ctx, target.SessionID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if resolved.Transport == nil || !resolved.Transport.IsAlive() {
		result.Error = ErrDisconnected.Error()
		return result
	}
	resolved.SessionID = target.SessionID
	if ctx.Err() != nil {
		result.Status = StatusCanceled
		result.Error = ctx.Err().Error()
		return result
	}
	raw := s.executeTargetTransport(ctx, Request{
		BatchID: batchID, SessionIDs: []string{target.SessionID}, Command: healthProbeCommand,
	}, resolved, timeout)
	result.SessionID = raw.SessionID
	result.LatencyMS = raw.DurationMS
	result.Status = raw.Status
	result.Error = raw.Error
	result.Reachable = raw.Status == StatusSuccess || raw.Status == StatusExitError
	if raw.Status != StatusSuccess {
		if result.Error == "" {
			result.Error = strings.TrimSpace(raw.Stderr)
		}
		if result.Error == "" {
			result.Error = fmt.Sprintf("health probe finished with status %s", raw.Status)
		}
		return result
	}
	if raw.Truncated {
		result.Error = "health probe output was truncated"
		return result
	}
	metrics := parseHealthOutput(raw.Stdout)
	result.UptimeSeconds = metrics.uptimeSeconds
	result.Load1 = metrics.load1
	result.Load5 = metrics.load5
	result.Load15 = metrics.load15
	result.DiskUsedPercent = metrics.diskUsedPercent
	result.MemoryUsedPercent = metrics.memoryUsedPercent
	result.DockerReclaimableBytes = metrics.dockerReclaimableBytes
	result.MetricErrors = metrics.errors
	return result
}

func validateHealthRequest(request ExecBatchHealthRequest) (time.Duration, int, error) {
	if request.BatchID == "" || len(request.BatchID) > 128 || strings.TrimSpace(request.BatchID) != request.BatchID {
		return 0, 0, fmt.Errorf("%w: batchId must be 1-128 non-space-padded characters", ErrInvalid)
	}
	if len(request.Targets) == 0 || len(request.Targets) > MaxTargets {
		return 0, 0, fmt.Errorf("%w: targets must contain 1-%d hosts", ErrInvalid, MaxTargets)
	}
	assets := make(map[string]struct{}, len(request.Targets))
	sessions := make(map[string]struct{}, len(request.Targets))
	for _, target := range request.Targets {
		if target.AssetID == "" || len(target.AssetID) > 191 || strings.TrimSpace(target.AssetID) != target.AssetID {
			return 0, 0, fmt.Errorf("%w: invalid health assetId", ErrInvalid)
		}
		if _, exists := assets[target.AssetID]; exists {
			return 0, 0, fmt.Errorf("%w: duplicate health assetId %s", ErrInvalid, target.AssetID)
		}
		assets[target.AssetID] = struct{}{}
		if target.SessionID == "" {
			continue
		}
		if len(target.SessionID) > 191 || strings.TrimSpace(target.SessionID) != target.SessionID {
			return 0, 0, fmt.Errorf("%w: invalid health sessionId", ErrInvalid)
		}
		if _, exists := sessions[target.SessionID]; exists {
			return 0, 0, fmt.Errorf("%w: duplicate health sessionId %s", ErrInvalid, target.SessionID)
		}
		sessions[target.SessionID] = struct{}{}
	}
	if request.TimeoutMS < 0 || request.TimeoutMS > MaxTimeout.Milliseconds() {
		return 0, 0, fmt.Errorf("%w: timeoutMs must be between 0 and %d", ErrInvalid, MaxTimeout.Milliseconds())
	}
	timeout := DefaultTimeout
	if request.TimeoutMS > 0 {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	if request.MaxConcurrent < 0 || request.MaxConcurrent > MaxConcurrency {
		return 0, 0, fmt.Errorf("%w: maxConcurrent must be between 0 and %d", ErrInvalid, MaxConcurrency)
	}
	concurrency := request.MaxConcurrent
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	return timeout, concurrency, nil
}
