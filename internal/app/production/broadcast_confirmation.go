package production

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	broadcastConfirmationRequiredKind = "broadcast_production_confirmation"
	maxBroadcastTargets               = 64
)

var errBroadcastConfirmationRequired = errors.New("broadcast production confirmation required")
var errBroadcastStaleProductionConfirmation = errors.New("broadcast production confirmation is stale")

type terminalBroadcastPrepareRequest struct {
	TabIDs                 []string                         `json:"tabIds"`
	Data                   []int                            `json:"data"`
	ProductionConfirmation *broadcastProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type broadcastProductionConfirmation struct {
	Confirmed     bool   `json:"confirmed"`
	TargetSetHash string `json:"targetSetHash"`
	InputHash     string `json:"inputHash"`
	// EnvironmentNames are the actual environment names the user typed back;
	// every required production environment name must be present.
	EnvironmentNames []string `json:"environmentNames,omitempty"`
}

type terminalBroadcastWriteOptions struct {
	TargetTabIDs           []string                         `json:"targetTabIds"`
	ProductionConfirmation *broadcastProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type broadcastConfirmationTarget struct {
	TabID       string `json:"tabId"`
	SessionID   string `json:"sessionId"`
	SessionName string `json:"sessionName"`
	AssetID     string `json:"assetId,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Environment string `json:"environment"`
}

type broadcastConfirmationRequiredDetail struct {
	Kind                     string                        `json:"kind"`
	TargetSetHash            string                        `json:"targetSetHash"`
	InputHash                string                        `json:"inputHash"`
	RequiredEnvironmentNames []string                      `json:"requiredEnvironmentNames"`
	ProductionTargets        []broadcastConfirmationTarget `json:"productionTargets"`
}

type broadcastConfirmationRequiredError struct {
	targetSetHash            string
	inputHash                string
	requiredEnvironmentNames []string
	productionTargets        []broadcastConfirmationTarget
}

func (e *broadcastConfirmationRequiredError) Error() string {
	return fmt.Sprintf("broadcast production confirmation required for %d targets", len(e.productionTargets))
}

func (e *broadcastConfirmationRequiredError) Unwrap() error {
	return errBroadcastConfirmationRequired
}

func (e *broadcastConfirmationRequiredError) ipcDetail() broadcastConfirmationRequiredDetail {
	return broadcastConfirmationRequiredDetail{
		Kind: broadcastConfirmationRequiredKind, TargetSetHash: e.targetSetHash, InputHash: e.inputHash,
		RequiredEnvironmentNames: append([]string(nil), e.requiredEnvironmentNames...),
		ProductionTargets:        append([]broadcastConfirmationTarget(nil), e.productionTargets...),
	}
}

type broadcastProductionChallenge struct {
	targetSetHash            string
	inputHash                string
	requiredEnvironmentNames []string
	productionTargets        []broadcastConfirmationTarget
}

type broadcastConfirmationAuditPayload struct {
	OwnerID           string                        `json:"ownerId"`
	TargetTabID       string                        `json:"targetTabId"`
	TargetSetHash     string                        `json:"targetSetHash"`
	InputHash         string                        `json:"inputHash"`
	EnvironmentNames  []string                      `json:"environmentNames"`
	ConfirmedAt       int64                         `json:"confirmedAt"`
	ProductionTargets []broadcastConfirmationTarget `json:"productionTargets"`
}

func normalizeBroadcastTabIDs(tabIDs []string, currentTabID string) error {
	if len(tabIDs) == 0 || len(tabIDs) > maxBroadcastTargets {
		return ipc.BadParam(fmt.Errorf("广播目标数量必须是 1-%d", maxBroadcastTargets))
	}
	seen := make(map[string]struct{}, len(tabIDs))
	currentIncluded := currentTabID == ""
	for _, tabID := range tabIDs {
		if strings.TrimSpace(tabID) == "" {
			return ipc.BadParam(errors.New("广播目标 tabId 不能为空"))
		}
		if _, exists := seen[tabID]; exists {
			return ipc.BadParam(fmt.Errorf("广播目标 tabId 重复：%s", tabID))
		}
		seen[tabID] = struct{}{}
		if tabID == currentTabID {
			currentIncluded = true
		}
	}
	if !currentIncluded {
		return ipc.BadParam(errors.New("当前写入终端不在广播目标集合中"))
	}
	return nil
}

// surfaceServices assembles the minimal ProductionServices the R-07 connect
// gate needs from the terminal command service's dependencies.
func (s *terminalCommandService) surfaceServices() *ProductionServices {
	services := &ProductionServices{
		Store: s.database, Sessions: s.sessions, hostKeys: s.hostKeys, desktop: s.desktop,
	}
	if s.sshConnector != nil {
		services.Vault = s.sshConnector.vault
	}
	return services
}

// connectDroppedBroadcastSession reconnects a dropped broadcast target's
// asset only with the broadcast surface's explicit opt-in and every R-07
// gate; without an opt-in the pre-R-07 behavior is kept (the write fails
// downstream as before). It returns nil without error when no opt-in exists.
// The gate is evidence-only here: a dropped session already exists, so the
// actual redial is the single-attempt in-place reconnect with the strict
// asset, which verifies host keys again and returns the actual dial error —
// never a background retry.
func (s *terminalCommandService) connectDroppedBroadcastSession(ctx context.Context, sessionID, assetID string) (*session.Session, error) {
	if err := authorizeSurfaceConnect(ctx, s.surfaceServices(), BroadcastConnectOptInSettingKeyForUser, BroadcastConnectProductionOptInSettingKeyForUser, ipc.OwnerID(ctx), assetID); err != nil {
		if !errors.Is(err, guard.ErrConnectOptInRequired) {
			return nil, err
		}
		return nil, nil
	}
	row, err := s.database.AssetGetLive(ctx, assetID)
	if err != nil {
		return nil, err
	}
	asset, err := productionSessionAsset(row, false, ipc.OwnerID(ctx))
	if err != nil {
		return nil, err
	}
	if options, ok := asset.Options.(map[string]any); ok {
		options["strictHostKey"] = true
	}
	if err := s.sessions.ReconnectOnceFor(ctx, sessionID, asset); err != nil {
		return nil, err
	}
	return s.sessions.SessionFor(ctx, sessionID)
}

func (s *terminalCommandService) resolveBroadcastTarget(ctx context.Context, tabID string) (broadcastConfirmationTarget, error) {
	target := broadcastConfirmationTarget{TabID: tabID}
	tab, err := s.sessions.TabFor(ctx, tabID)
	var connected *session.Session
	if err == nil {
		target.SessionID = tab.SessionID
		connected, err = s.sessions.SessionFor(ctx, tab.SessionID)
	}
	if errors.Is(err, session.ErrTabNotFound) && s.docker != nil {
		sessionID, dockerErr := s.docker.StreamSession(tabID)
		if dockerErr == nil {
			target.SessionID = sessionID
			connected, err = s.sessions.SessionFor(ctx, sessionID)
		}
	}
	if err != nil {
		return broadcastConfirmationTarget{}, err
	}
	if connected == nil {
		return broadcastConfirmationTarget{}, session.ErrTabNotFound
	}
	if connected.Info().Status != session.StatusConnected && connected.Info().AssetID != "" && connected.Asset().StoreBacked {
		reconnected, err := s.connectDroppedBroadcastSession(ctx, target.SessionID, connected.Info().AssetID)
		if err != nil {
			return broadcastConfirmationTarget{}, err
		}
		if reconnected != nil {
			connected = reconnected
		}
	}
	info := connected.Info()
	target.SessionName = info.Name
	target.AssetID = info.AssetID
	if info.AssetID == "" || !connected.Asset().StoreBacked {
		return target, nil
	}
	if s.database == nil {
		return broadcastConfirmationTarget{}, fmt.Errorf("resolve broadcast asset environment for %s: asset reader unavailable", tabID)
	}
	asset, err := s.database.AssetGetLive(ctx, info.AssetID)
	if err != nil {
		return broadcastConfirmationTarget{}, fmt.Errorf("resolve broadcast asset environment for %s: %w", tabID, err)
	}
	target.Environment = store.AssetEnvironment(asset)
	if asset.Host != nil {
		target.Hostname = *asset.Host
	}
	return target, nil
}

func (s *terminalCommandService) resolveBroadcastTargets(ctx context.Context, tabIDs []string) ([]broadcastConfirmationTarget, error) {
	targets := make([]broadcastConfirmationTarget, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		target, err := s.resolveBroadcastTarget(ctx, tabID)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func newBroadcastProductionChallenge(targets []broadcastConfirmationTarget, data []byte, isProduction productionEnvironmentClassifier) (*broadcastProductionChallenge, error) {
	productionTargets := make([]broadcastConfirmationTarget, 0)
	productionEnvironments := make([]string, 0, len(targets))
	for _, target := range targets {
		production, err := isProduction(target.Environment)
		if err != nil {
			return nil, err
		}
		if production {
			productionTargets = append(productionTargets, target)
			productionEnvironments = append(productionEnvironments, target.Environment)
		}
	}
	if len(productionTargets) == 0 {
		return nil, nil
	}
	allTargets := append([]broadcastConfirmationTarget(nil), targets...)
	sort.Slice(allTargets, func(i, j int) bool {
		if allTargets[i].TabID == allTargets[j].TabID {
			return allTargets[i].SessionID < allTargets[j].SessionID
		}
		return allTargets[i].TabID < allTargets[j].TabID
	})
	sort.Slice(productionTargets, func(i, j int) bool { return productionTargets[i].TabID < productionTargets[j].TabID })
	canonical, _ := json.Marshal(struct {
		Targets []broadcastConfirmationTarget `json:"targets"`
	}{Targets: allTargets})
	return &broadcastProductionChallenge{
		targetSetHash: broadcastDigest("target-set-v2", canonical), inputHash: broadcastDigest("input-v1", data),
		requiredEnvironmentNames: productionEnvironmentNames(productionEnvironments),
		productionTargets:        productionTargets,
	}, nil
}

func validateBroadcastProductionConfirmation(confirmation *broadcastProductionConfirmation, challenge *broadcastProductionChallenge) error {
	if challenge == nil {
		if confirmation != nil {
			return fmt.Errorf("%w: current target set has no production challenge; preflight again without the old confirmation", errBroadcastStaleProductionConfirmation)
		}
		return nil
	}
	if confirmation != nil && confirmation.Confirmed &&
		confirmation.TargetSetHash == challenge.targetSetHash && confirmation.InputHash == challenge.inputHash &&
		environmentNamesMatch(confirmation.EnvironmentNames, challenge.requiredEnvironmentNames) {
		return nil
	}
	return &broadcastConfirmationRequiredError{
		targetSetHash: challenge.targetSetHash, inputHash: challenge.inputHash,
		requiredEnvironmentNames: append([]string(nil), challenge.requiredEnvironmentNames...),
		productionTargets:        append([]broadcastConfirmationTarget(nil), challenge.productionTargets...),
	}
}

func (s *terminalCommandService) prepareBroadcast(ctx context.Context, tabIDs []string, data []byte, confirmation *broadcastProductionConfirmation) error {
	if err := normalizeBroadcastTabIDs(tabIDs, ""); err != nil {
		return err
	}
	targets, err := s.resolveBroadcastTargets(ctx, tabIDs)
	if err != nil {
		return err
	}
	challenge, err := newBroadcastProductionChallenge(targets, data, s.classifyProductionEnvironments(ctx))
	if err != nil {
		return err
	}
	return validateBroadcastProductionConfirmation(confirmation, challenge)
}

func (s *terminalCommandService) authorizeBroadcastWrite(ctx context.Context, tabID string, data []byte, options *terminalBroadcastWriteOptions) error {
	if options == nil {
		return nil
	}
	if err := normalizeBroadcastTabIDs(options.TargetTabIDs, tabID); err != nil {
		return err
	}
	current, err := s.resolveBroadcastTarget(ctx, tabID)
	if err != nil {
		return err
	}
	targets, err := s.resolveBroadcastTargets(ctx, options.TargetTabIDs)
	if err != nil {
		return err
	}
	challenge, err := newBroadcastProductionChallenge(targets, data, s.classifyProductionEnvironments(ctx))
	if err != nil {
		return err
	}
	if err := validateBroadcastProductionConfirmation(options.ProductionConfirmation, challenge); err != nil {
		return err
	}
	if challenge == nil {
		return nil
	}
	if s.database == nil {
		return errors.New("record broadcast production confirmation: audit unavailable")
	}
	auditCtx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(ctx), ipc.OwnerID(ctx)), 5*time.Second)
	defer cancel()
	return s.database.AuditInsert(auditCtx, store.AuditInput{
		SessionID: broadcastStringPointer(current.SessionID), AssetID: broadcastStringPointer(current.AssetID),
		Source: "user", Kind: broadcastConfirmationRequiredKind,
		Payload: broadcastConfirmationAuditPayload{
			OwnerID: ipc.OwnerID(ctx), TargetTabID: tabID, TargetSetHash: challenge.targetSetHash,
			InputHash: challenge.inputHash, EnvironmentNames: append([]string(nil), challenge.requiredEnvironmentNames...),
			ConfirmedAt:       time.Now().UnixMilli(),
			ProductionTargets: append([]broadcastConfirmationTarget(nil), challenge.productionTargets...),
		},
	})
}

func broadcastConfirmationIPCError(err error) error {
	if err == nil {
		return nil
	}
	var confirmation *broadcastConfirmationRequiredError
	if errors.As(err, &confirmation) {
		return ipc.WrapError(ipc.CodeNeedsConfirm, "生产环境广播需要输入实际环境名确认", err).WithDetail(confirmation.ipcDetail())
	}
	if errors.Is(err, errBroadcastStaleProductionConfirmation) {
		return ipc.WrapError(ipc.Code("conflict"), "生产确认已 stale，请重新 preflight：目标或环境已变化，请移除旧确认后重试", err)
	}
	// R-07 connect-gate denials are actionable authorization outcomes, not
	// internal faults: surface each with a distinct code and visible reason.
	if errors.Is(err, guard.ErrConnectHostKeyUnknown) || errors.Is(err, guard.ErrConnectHostKeyChanged) {
		return ipc.WrapError(ipc.CodeHostKeyPending, err.Error(), err)
	}
	if errors.Is(err, guard.ErrConnectVaultUnavailable) {
		return ipc.WrapError(ipc.CodeVaultLocked, err.Error(), err)
	}
	if errors.Is(err, guard.ErrConnectProductionConfirmationRequired) {
		return ipc.WrapError(ipc.CodeNeedsConfirm, err.Error(), err)
	}
	if errors.Is(err, guard.ErrConnectOwnerMismatch) {
		return ipc.WrapError(ipc.CodeForbidden, err.Error(), err)
	}
	return terminalIPCError(err)
}

func broadcastDigest(domain string, value []byte) string {
	input := make([]byte, 0, len("nexterm.broadcast.")+len(domain)+1+len(value))
	input = append(input, "nexterm.broadcast."...)
	input = append(input, domain...)
	input = append(input, 0)
	input = append(input, value...)
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

func broadcastStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
