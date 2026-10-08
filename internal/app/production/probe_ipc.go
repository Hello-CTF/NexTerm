package production

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/ssh"
)

const (
	maxAssetProbeBatch     = 256
	defaultAssetProbeLimit = 8
	maxAssetProbeLimit     = 16
)

type hostKeyProbeRequest struct {
	AssetID   string `json:"assetId"`
	TimeoutMS int64  `json:"timeoutMs"`
}

type hostKeyProbeKnownDTO struct {
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

type hostKeyProbeDTO struct {
	Host        string                 `json:"host"`
	Port        int                    `json:"port"`
	KeyType     string                 `json:"keyType"`
	Fingerprint string                 `json:"fingerprint"`
	State       string                 `json:"state"`
	Known       []hostKeyProbeKnownDTO `json:"known,omitempty"`
}

type assetProbeBatchRequest struct {
	AssetIDs      []string `json:"assetIds"`
	TimeoutMS     int64    `json:"timeoutMs"`
	MaxConcurrent int      `json:"maxConcurrent"`
}

type assetProbeResultDTO struct {
	AssetID    string `json:"assetId"`
	Reachable  bool   `json:"reachable"`
	Kind       string `json:"kind,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"durationMs"`
}

type assetProbeBatchDTO struct {
	Results []assetProbeResultDTO `json:"results"`
}

type sshConnectErrorDetail struct {
	Kind  string `json:"kind"`
	Hint  string `json:"hint"`
	Op    string `json:"op,omitempty"`
	Host  string `json:"host,omitempty"`
	Port  int    `json:"port,omitempty"`
	Proxy string `json:"proxy,omitempty"`
	Jump  string `json:"jump,omitempty"`
}

type sshHostKeyErrorDetail struct {
	Host        string                 `json:"host"`
	Port        int                    `json:"port"`
	KeyType     string                 `json:"keyType"`
	Fingerprint string                 `json:"fingerprint"`
	Changed     bool                   `json:"changed"`
	Known       []hostKeyProbeKnownDTO `json:"known,omitempty"`
	Kind        string                 `json:"kind"`
	Hint        string                 `json:"hint"`
}

func (s *terminalCommandService) registerSSHDiagnostics(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "session_probe_host_key", func(ctx context.Context, _ *ipc.Call, input hostKeyProbeRequest) (hostKeyProbeDTO, error) {
				row, kind, err := s.sshProbeAsset(ctx, input.AssetID)
				if err != nil {
					return hostKeyProbeDTO{}, err
				}
				if kind != session.KindSSH && kind != session.KindDocker {
					return hostKeyProbeDTO{}, ipc.BadParam(fmt.Errorf("asset kind %s does not use SSH host keys", kind))
				}
				if s.hostKeys == nil || s.sshConnector == nil {
					return hostKeyProbeDTO{}, ipc.NewError(ipc.CodeUnsupported, "SSH host key diagnostics are unavailable")
				}
				options, err := productionAssetOptionsFromRow(row)
				if err != nil {
					return hostKeyProbeDTO{}, err
				}
				config, err := s.sshConnector.sshConfig(ctx, options, 0, []string{row.ID})
				if err != nil {
					return hostKeyProbeDTO{}, err
				}
				stripProbeAgentForwarding(&config)
				config.ConnectTimeout = probeTimeout(input.TimeoutMS, options.ConnectTimeout, 10*time.Second)
				result, err := ssh.ProbeHostKey(ctx, config)
				if err != nil {
					return hostKeyProbeDTO{}, sshDiagnosticsIPCError(err)
				}
				dto := hostKeyProbeDTO{
					Host: result.Host, Port: result.Port, KeyType: result.KeyType,
					Fingerprint: result.Fingerprint, State: string(result.State),
				}
				for _, known := range result.Known {
					dto.Known = append(dto.Known, hostKeyProbeKnownDTO{KeyType: known.KeyType, Fingerprint: known.Fingerprint})
				}
				return dto, nil
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "asset_probe_batch", func(ctx context.Context, _ *ipc.Call, input assetProbeBatchRequest) (assetProbeBatchDTO, error) {
				return s.probeAssetBatch(ctx, input)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func (s *terminalCommandService) sshProbeAsset(ctx context.Context, assetID string) (store.AssetRow, string, error) {
	if assetID == "" {
		return store.AssetRow{}, "", ipc.BadParam(fmt.Errorf("asset id is required"))
	}
	row, err := s.database.AssetGet(ctx, assetID)
	if err != nil {
		return store.AssetRow{}, "", err
	}
	return row, row.Kind, nil
}

func stripProbeAgentForwarding(config *ssh.Config) {
	config.ForwardAgent = false
	if config.Jump != nil {
		stripProbeAgentForwarding(config.Jump)
	}
}

func probeTimeout(timeoutMS, assetTimeoutSeconds int64, fallback time.Duration) time.Duration {
	if timeoutMS > 0 {
		return clampProbeTimeout(timeoutMS, fallback)
	}
	if assetTimeoutSeconds > 0 {
		return clampProbeTimeout(assetTimeoutSeconds*1000, fallback)
	}
	return fallback
}

func (s *terminalCommandService) probeAssetBatch(ctx context.Context, input assetProbeBatchRequest) (assetProbeBatchDTO, error) {
	if len(input.AssetIDs) == 0 {
		return assetProbeBatchDTO{}, ipc.BadParam(fmt.Errorf("asset probe batch requires at least one asset id"))
	}
	if len(input.AssetIDs) > maxAssetProbeBatch {
		return assetProbeBatchDTO{}, ipc.BadParam(fmt.Errorf("asset probe batch is limited to %d assets", maxAssetProbeBatch))
	}
	timeout := clampProbeTimeout(input.TimeoutMS, 5*time.Second)
	limit := input.MaxConcurrent
	if limit <= 0 {
		limit = defaultAssetProbeLimit
	}
	if limit > maxAssetProbeLimit {
		limit = maxAssetProbeLimit
	}

	assetIDs := dedupeAssetIDs(input.AssetIDs)
	targets := make([]assetProbeTarget, len(assetIDs))
	for index, assetID := range assetIDs {
		targets[index] = s.resolveAssetProbeTarget(ctx, assetID)
	}
	if limit > len(targets) {
		limit = len(targets)
	}

	results := make([]assetProbeResultDTO, len(targets))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range limit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = s.probeAssetTarget(ctx, targets[index], timeout)
			}
		}()
	}
	for index := range targets {
		if ctx.Err() != nil {
			results[index] = canceledAssetProbeResult(targets[index].assetID)
			continue
		}
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return assetProbeBatchDTO{Results: results}, nil
}

type assetProbeTarget struct {
	assetID string
	kind    string
	options productionAssetOptions
	failure *assetProbeResultDTO
}

func (s *terminalCommandService) resolveAssetProbeTarget(ctx context.Context, assetID string) assetProbeTarget {
	target := assetProbeTarget{assetID: assetID}
	row, err := s.database.AssetGet(ctx, assetID)
	if err != nil {
		target.failure = &assetProbeResultDTO{AssetID: assetID, Kind: "not_found", Error: "asset not found"}
		return target
	}
	target.kind = row.Kind
	options, err := productionAssetOptionsFromRow(row)
	if err != nil {
		target.failure = &assetProbeResultDTO{AssetID: assetID, Kind: "config", Error: "asset options are invalid"}
		return target
	}
	target.options = options
	return target
}

func (s *terminalCommandService) probeAssetTarget(ctx context.Context, target assetProbeTarget, timeout time.Duration) (result assetProbeResultDTO) {
	result.AssetID = target.assetID
	if target.failure != nil {
		return *target.failure
	}
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	if target.kind == session.KindLocal {
		result.Reachable = true
		return result
	}
	switch target.kind {
	case session.KindSSH, session.KindDocker, session.KindWinRM, "mysql", "redis":
	default:
		result.Kind = "unsupported"
		result.Error = fmt.Sprintf("asset kind %s cannot be probed", target.kind)
		return result
	}
	if target.options.Host == "" {
		result.Kind = "config"
		result.Error = "asset host is required"
		return result
	}
	err := s.probeSSHReachability(ctx, target, timeout)
	if err == nil {
		result.Reachable = true
		return result
	}
	if ctx.Err() != nil {
		return canceledAssetProbeResult(target.assetID)
	}
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		result.Kind = string(connectErr.Kind)
		result.Error = connectErr.UserMessage()
		slog.Warn("asset probe failed", "asset", target.assetID, "kind", result.Kind, "error", connectErr)
		return result
	}
	result.Kind = probeFailureKind(err)
	result.Error = err.Error()
	return result
}

func probeFailureKind(err error) string {
	var appErr *ipc.Error
	if !errors.As(err, &appErr) {
		return "internal"
	}
	switch appErr.Code {
	case ipc.CodeBadParam:
		return "config"
	case ipc.CodeUnsupported:
		return "unsupported"
	case ipc.CodeNotFound:
		return "not_found"
	case ipc.CodeVaultLocked, ipc.CodeVaultNotInit, ipc.CodeBadMasterPass, ipc.CodeDecrypt:
		return "vault"
	}
	return "internal"
}

func (s *terminalCommandService) probeSSHReachability(ctx context.Context, target assetProbeTarget, timeout time.Duration) error {
	if target.options.JumpAssetID != "" {
		if s.sshConnector == nil {
			return ipc.NewError(ipc.CodeUnsupported, "SSH jump chain diagnostics are unavailable")
		}
		config, err := s.sshConnector.sshConfig(ctx, target.options, 0, []string{target.assetID})
		if err != nil {
			return err
		}
		stripProbeAgentForwarding(&config)
		return ssh.ProbeReachability(ctx, config, timeout)
	}
	port := target.options.Port
	if target.kind != session.KindSSH && target.kind != session.KindDocker && port == 0 {
		switch target.kind {
		case session.KindWinRM:
			port = 5985
		case "mysql":
			port = 3306
		case "redis":
			port = 6379
		}
	}
	return ssh.ProbeReachability(ctx, ssh.Config{
		Host: target.options.Host, Port: port,
		ProxyURL: target.options.Proxy, ProxyCommand: target.options.ProxyCommand,
	}, timeout)
}

func canceledAssetProbeResult(assetID string) assetProbeResultDTO {
	return assetProbeResultDTO{AssetID: assetID, Kind: "canceled", Error: "asset probe canceled"}
}

func dedupeAssetIDs(assetIDs []string) []string {
	seen := make(map[string]struct{}, len(assetIDs))
	deduped := make([]string, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		if _, ok := seen[assetID]; ok {
			continue
		}
		seen[assetID] = struct{}{}
		deduped = append(deduped, assetID)
	}
	return deduped
}

func clampProbeTimeout(timeoutMS int64, fallback time.Duration) time.Duration {
	if timeoutMS <= 0 {
		return fallback
	}
	if timeoutMS >= 30000 {
		return 30 * time.Second
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func sshDiagnosticsIPCError(err error) *ipc.Error {
	var connectErr *ssh.ConnectError
	if errors.As(err, &connectErr) {
		return sshConnectIPCError(connectErr)
	}
	return ipc.NormalizeError(err)
}

func sshConnectIPCError(connectErr *ssh.ConnectError) *ipc.Error {
	var keyErr *ssh.HostKeyError
	if errors.As(connectErr, &keyErr) {
		detail := sshHostKeyErrorDetail{
			Host: keyErr.Presented.Host, Port: keyErr.Presented.Port,
			KeyType: keyErr.Presented.KeyType, Fingerprint: keyErr.Presented.Fingerprint,
			Changed: !keyErr.Pending, Kind: string(connectErr.Kind), Hint: connectErr.Hint,
		}
		for _, known := range keyErr.Known {
			detail.Known = append(detail.Known, hostKeyProbeKnownDTO{KeyType: known.KeyType, Fingerprint: known.Fingerprint})
		}
		return ipc.WrapError(ipc.CodeHostKeyPending, keyErr.UserMessage(), connectErr).WithDetail(detail)
	}
	code := ipc.CodeSSH
	switch connectErr.Kind {
	case ssh.ErrorKindConfig:
		code = ipc.CodeBadParam
	case ssh.ErrorKindTimeout:
		code = ipc.CodeTimeout
	}
	detail := sshConnectErrorDetail{
		Kind: string(connectErr.Kind), Hint: connectErr.Hint, Op: connectErr.Op,
		Host: connectErr.Host, Port: connectErr.Port, Proxy: connectErr.Proxy, Jump: connectErr.Jump,
	}
	return ipc.WrapError(code, connectErr.UserMessage(), connectErr).WithDetail(detail)
}
