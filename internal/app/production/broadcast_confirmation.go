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

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	broadcastConfirmationRequiredKind = "broadcast_production_confirmation"
	maxBroadcastTargets               = 64
)

var errBroadcastConfirmationRequired = errors.New("broadcast production confirmation required")

type terminalBroadcastPrepareRequest struct {
	TabIDs                 []string                         `json:"tabIds"`
	Data                   []int                            `json:"data"`
	ProductionConfirmation *broadcastProductionConfirmation `json:"productionConfirmation,omitempty"`
}

type broadcastProductionConfirmation struct {
	Confirmed     bool   `json:"confirmed"`
	TargetSetHash string `json:"targetSetHash"`
	InputHash     string `json:"inputHash"`
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
	Kind              string                        `json:"kind"`
	TargetSetHash     string                        `json:"targetSetHash"`
	InputHash         string                        `json:"inputHash"`
	ProductionTargets []broadcastConfirmationTarget `json:"productionTargets"`
}

type broadcastConfirmationRequiredError struct {
	targetSetHash     string
	inputHash         string
	productionTargets []broadcastConfirmationTarget
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
		ProductionTargets: append([]broadcastConfirmationTarget(nil), e.productionTargets...),
	}
}

type broadcastProductionChallenge struct {
	targetSetHash     string
	inputHash         string
	productionTargets []broadcastConfirmationTarget
}

type broadcastConfirmationAuditPayload struct {
	OwnerID           string                        `json:"ownerId"`
	TargetTabID       string                        `json:"targetTabId"`
	TargetSetHash     string                        `json:"targetSetHash"`
	InputHash         string                        `json:"inputHash"`
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

func newBroadcastProductionChallenge(targets []broadcastConfirmationTarget, data []byte) *broadcastProductionChallenge {
	productionTargets := make([]broadcastConfirmationTarget, 0)
	for _, target := range targets {
		if target.Environment == "production" {
			productionTargets = append(productionTargets, target)
		}
	}
	if len(productionTargets) == 0 {
		return nil
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
		productionTargets: productionTargets,
	}
}

func validateBroadcastProductionConfirmation(confirmation *broadcastProductionConfirmation, challenge *broadcastProductionChallenge) error {
	if challenge == nil {
		return nil
	}
	if confirmation != nil && confirmation.Confirmed &&
		confirmation.TargetSetHash == challenge.targetSetHash && confirmation.InputHash == challenge.inputHash {
		return nil
	}
	return &broadcastConfirmationRequiredError{
		targetSetHash: challenge.targetSetHash, inputHash: challenge.inputHash,
		productionTargets: append([]broadcastConfirmationTarget(nil), challenge.productionTargets...),
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
	return validateBroadcastProductionConfirmation(confirmation, newBroadcastProductionChallenge(targets, data))
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
	if current.Environment != "production" {
		return nil
	}
	targets, err := s.resolveBroadcastTargets(ctx, options.TargetTabIDs)
	if err != nil {
		return err
	}
	challenge := newBroadcastProductionChallenge(targets, data)
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
			InputHash: challenge.inputHash, ConfirmedAt: time.Now().UnixMilli(),
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
		return ipc.WrapError(ipc.CodeNeedsConfirm, "生产环境广播需要显式确认", err).WithDetail(confirmation.ipcDetail())
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
