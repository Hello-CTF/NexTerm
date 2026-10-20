package forward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// MaxConcurrentRecoveryConnections caps how many devices startup recovery
// connects at the same time.
const MaxConcurrentRecoveryConnections = 4

// RecoveryDevice describes one device startup recovery may reconnect to so
// the device's auto-start rules can listen again.
type RecoveryDevice struct {
	AssetID string
	// Connect establishes a session for the asset and returns its session ID.
	Connect func(ctx context.Context) (sessionID string, err error)
}

// RecoveryOutcome reports what startup recovery did for one device.
type RecoveryOutcome struct {
	AssetID   string
	SessionID string   // empty when the device was skipped or failed to connect
	Started   []string // rule IDs confirmed active after recovery
	Skipped   string   // non-empty when the device was deliberately skipped
	Err       error    // set when connecting or starting a rule failed
}

// StartAutoStartRules starts every auto-start rule bound to the session's
// asset. Saved rules always listen on loopback, so auto-start never exposes a
// listener beyond 127.0.0.1.
func (s *Service) StartAutoStartRules(ctx context.Context, sessionID string) ([]Rule, error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	rules, err := s.ruleStore()
	if err != nil {
		return nil, err
	}
	hostID, err := s.hostFor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	rows, err := rules.ForwardRuleList(ctx)
	if err != nil {
		return nil, err
	}
	started := make([]Rule, 0, len(rows))
	var errs []error
	for _, row := range rows {
		if !row.AutoStart || row.AssetID != hostID {
			continue
		}
		rule, err := s.StartRule(ctx, row.ID, sessionID)
		if err != nil {
			errs = append(errs, fmt.Errorf("自动启动转发规则 %s: %w", row.ID, err))
			continue
		}
		started = append(started, rule)
	}
	return started, errors.Join(errs...)
}

// RecoverAutoStartRules reconnects the given devices and starts their
// auto-start rules. Recovery is opt-in and default-off: it only runs when the
// caller explicitly invokes it, and per device it only starts rules whose
// autoStart flag is set. At most MaxConcurrentRecoveryConnections devices are
// connected concurrently. Production assets are skipped.
func (s *Service) RecoverAutoStartRules(ctx context.Context, devices []RecoveryDevice) []RecoveryOutcome {
	outcomes := make([]RecoveryOutcome, len(devices))
	if len(devices) == 0 {
		return outcomes
	}
	failAll := func(err error) []RecoveryOutcome {
		for index, device := range devices {
			outcomes[index] = RecoveryOutcome{AssetID: device.AssetID, Err: err}
		}
		return outcomes
	}
	if err := s.available(); err != nil {
		return failAll(err)
	}
	rules, err := s.ruleStore()
	if err != nil {
		return failAll(err)
	}
	rows, err := rules.ForwardRuleList(ctx)
	if err != nil {
		return failAll(err)
	}
	autoStartByAsset := make(map[string]int)
	for _, row := range rows {
		if row.AutoStart {
			autoStartByAsset[row.AssetID]++
		}
	}
	seen := make(map[string]struct{}, len(devices))
	semaphore := make(chan struct{}, MaxConcurrentRecoveryConnections)
	var wg sync.WaitGroup
	for index, device := range devices {
		assetID := strings.TrimSpace(device.AssetID)
		if assetID == "" {
			outcomes[index] = RecoveryOutcome{Err: ipc.BadParam(errors.New("恢复设备资产 ID 不能为空"))}
			continue
		}
		if device.Connect == nil {
			outcomes[index] = RecoveryOutcome{AssetID: assetID, Err: ipc.BadParam(errors.New("恢复设备缺少连接接口"))}
			continue
		}
		if _, duplicate := seen[assetID]; duplicate {
			outcomes[index] = RecoveryOutcome{AssetID: assetID, Skipped: "重复设备"}
			continue
		}
		seen[assetID] = struct{}{}
		if autoStartByAsset[assetID] == 0 {
			outcomes[index] = RecoveryOutcome{AssetID: assetID, Skipped: "没有自动启动规则"}
			continue
		}
		environment, err := rules.ForwardRuleAssetEnvironment(ctx, assetID)
		if err != nil {
			outcomes[index] = RecoveryOutcome{AssetID: assetID, Err: err}
			continue
		}
		if environment == "production" {
			outcomes[index] = RecoveryOutcome{AssetID: assetID, Skipped: "生产环境资产不参与启动恢复"}
			continue
		}
		wg.Add(1)
		go func(index int, assetID string, connect func(context.Context) (string, error)) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				outcomes[index] = RecoveryOutcome{AssetID: assetID, Err: ctx.Err()}
				return
			}
			sessionID, err := connect(ctx)
			if err != nil {
				outcomes[index] = RecoveryOutcome{AssetID: assetID, Err: fmt.Errorf("恢复设备连接失败: %w", err)}
				return
			}
			hostID, err := s.hostFor(ctx, sessionID)
			if err != nil {
				outcomes[index] = RecoveryOutcome{AssetID: assetID, SessionID: sessionID, Err: err}
				return
			}
			if hostID != assetID {
				outcomes[index] = RecoveryOutcome{
					AssetID: assetID, SessionID: sessionID,
					Err: ipc.NewError(ipc.CodeBadParam, "恢复设备会话不属于目标资产"),
				}
				return
			}
			started, err := s.StartAutoStartRules(ctx, sessionID)
			outcome := RecoveryOutcome{AssetID: assetID, SessionID: sessionID, Err: err}
			for _, rule := range started {
				outcome.Started = append(outcome.Started, rule.ID)
			}
			outcomes[index] = outcome
			if err != nil {
				s.report(fmt.Errorf("设备 %s 自动启动恢复未完成: %w", assetID, err))
			}
		}(index, assetID, device.Connect)
	}
	wg.Wait()
	return outcomes
}
