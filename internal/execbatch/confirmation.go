package execbatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const ConfirmationRequiredKind = "exec_batch_production_confirmation"

var ErrConfirmationRequired = errors.New("exec batch production confirmation required")
var ErrStaleProductionConfirmation = errors.New("exec batch production confirmation is stale")

type ProductionConfirmation struct {
	Confirmed     bool   `json:"confirmed"`
	TargetSetHash string `json:"targetSetHash"`
	CommandHash   string `json:"commandHash"`
	// EnvironmentNames are the actual environment names the user typed back;
	// every required production environment name must be present.
	EnvironmentNames []string `json:"environmentNames,omitempty"`
}

type ConfirmationTarget struct {
	SessionID   string `json:"sessionId"`
	SessionName string `json:"sessionName"`
	AssetID     string `json:"assetId,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Environment string `json:"environment"`
}

type ConfirmationRequiredDetail struct {
	Kind                     string               `json:"kind"`
	BatchID                  string               `json:"batchId"`
	TargetSetHash            string               `json:"targetSetHash"`
	CommandHash              string               `json:"commandHash"`
	RequiredEnvironmentNames []string             `json:"requiredEnvironmentNames"`
	ProductionTargets        []ConfirmationTarget `json:"productionTargets"`
}

type ConfirmationRequiredError struct {
	BatchID                  string
	TargetSetHash            string
	CommandHash              string
	RequiredEnvironmentNames []string
	ProductionTargets        []ConfirmationTarget
}

func (e *ConfirmationRequiredError) Error() string {
	return fmt.Sprintf("exec batch production confirmation required for %d targets", len(e.ProductionTargets))
}

func (e *ConfirmationRequiredError) Unwrap() error {
	return ErrConfirmationRequired
}

func (e *ConfirmationRequiredError) IPCDetail() ConfirmationRequiredDetail {
	return ConfirmationRequiredDetail{
		Kind: ConfirmationRequiredKind, BatchID: e.BatchID, TargetSetHash: e.TargetSetHash,
		CommandHash:              e.CommandHash,
		RequiredEnvironmentNames: append([]string(nil), e.RequiredEnvironmentNames...),
		ProductionTargets:        append([]ConfirmationTarget(nil), e.ProductionTargets...),
	}
}

type productionChallenge struct {
	batchID                  string
	targetSetHash            string
	commandHash              string
	requiredEnvironmentNames []string
	productionTargets        []ConfirmationTarget
}

type productionConfirmationAuditPayload struct {
	OwnerID           string               `json:"ownerId"`
	BatchID           string               `json:"batchId"`
	TargetSetHash     string               `json:"targetSetHash"`
	CommandHash       string               `json:"commandHash"`
	EnvironmentNames  []string             `json:"environmentNames"`
	ConfirmedAt       int64                `json:"confirmedAt"`
	ProductionTargets []ConfirmationTarget `json:"productionTargets"`
}

// newProductionChallenge classifies targets through the owner's configured
// production environment list (guard settings); the owner ID is bound into the
// target-set hash so one owner's confirmation never authorizes another owner.
func (s *Service) newProductionChallenge(ctx context.Context, ownerID, batchID, command string, targets []Target) (*productionChallenge, error) {
	classificationCtx := ctx
	if ownerID != "" {
		classificationCtx = ipc.WithUserID(ctx, ownerID)
	}
	allTargets := make([]ConfirmationTarget, 0, len(targets))
	productionTargets := make([]ConfirmationTarget, 0)
	productionEnvironments := make([]string, 0, len(targets))
	for _, target := range targets {
		item := ConfirmationTarget{
			SessionID: target.SessionID, SessionName: target.SessionName, AssetID: target.AssetID,
			Hostname: target.Hostname, Environment: target.Environment,
		}
		allTargets = append(allTargets, item)
		production, err := guard.IsProductionEnvironment(classificationCtx, s.productionSettings, target.Environment)
		if err != nil {
			return nil, err
		}
		if production {
			productionTargets = append(productionTargets, item)
			productionEnvironments = append(productionEnvironments, target.Environment)
		}
	}
	if len(productionTargets) == 0 {
		return nil, nil
	}
	sort.Slice(allTargets, func(i, j int) bool {
		if allTargets[i].SessionID == allTargets[j].SessionID {
			return allTargets[i].AssetID < allTargets[j].AssetID
		}
		return allTargets[i].SessionID < allTargets[j].SessionID
	})
	sort.Slice(productionTargets, func(i, j int) bool {
		return productionTargets[i].SessionID < productionTargets[j].SessionID
	})
	canonical, _ := json.Marshal(struct {
		BatchID string               `json:"batchId"`
		OwnerID string               `json:"ownerId"`
		Targets []ConfirmationTarget `json:"targets"`
	}{BatchID: batchID, OwnerID: ownerID, Targets: allTargets})
	return &productionChallenge{
		batchID: batchID, targetSetHash: digest("target-set-v2", canonical), commandHash: commandDigest(command),
		requiredEnvironmentNames: guard.ProductionEnvironmentNames(productionEnvironments),
		productionTargets:        productionTargets,
	}, nil
}

func validateProductionConfirmation(confirmation *ProductionConfirmation, challenge *productionChallenge) error {
	if challenge == nil {
		if confirmation != nil {
			return fmt.Errorf("%w: current target set has no production challenge; preflight again without the old confirmation", ErrStaleProductionConfirmation)
		}
		return nil
	}
	if confirmation != nil && confirmation.Confirmed &&
		confirmation.TargetSetHash == challenge.targetSetHash && confirmation.CommandHash == challenge.commandHash &&
		guard.EnvironmentNamesMatch(confirmation.EnvironmentNames, challenge.requiredEnvironmentNames) {
		return nil
	}
	return &ConfirmationRequiredError{
		BatchID: challenge.batchID, TargetSetHash: challenge.targetSetHash, CommandHash: challenge.commandHash,
		RequiredEnvironmentNames: append([]string(nil), challenge.requiredEnvironmentNames...),
		ProductionTargets:        append([]ConfirmationTarget(nil), challenge.productionTargets...),
	}
}

func (s *Service) auditProductionConfirmation(ctx context.Context, ownerID string, request Request, challenge *productionChallenge) error {
	if challenge == nil {
		return nil
	}
	if s.auditor == nil {
		return fmt.Errorf("record exec batch production confirmation %s: audit unavailable", request.BatchID)
	}
	auditCtx, cancel := context.WithTimeout(execAuditContext(ctx, ownerID), 5*time.Second)
	defer cancel()
	return s.auditor.AuditInsert(auditCtx, store.AuditInput{
		Source: "user", Kind: "exec_batch_production_confirmation",
		Payload: productionConfirmationAuditPayload{
			OwnerID: ownerID, BatchID: request.BatchID, TargetSetHash: challenge.targetSetHash,
			CommandHash: challenge.commandHash, EnvironmentNames: append([]string(nil), challenge.requiredEnvironmentNames...),
			ConfirmedAt:       s.now().UnixMilli(),
			ProductionTargets: append([]ConfirmationTarget(nil), challenge.productionTargets...),
		},
	})
}

func execAuditContext(ctx context.Context, ownerID string) context.Context {
	return ipc.WithUserID(context.WithoutCancel(ctx), ownerID)
}

// isProductionEnvironment backs the history row's Production flag with the
// built-in names; production write challenges use the owner's configured list
// via guard.IsProductionEnvironment instead.
func isProductionEnvironment(environment string) bool {
	return guard.IsBuiltInProductionEnvironment(environment)
}

func commandDigest(command string) string {
	return digest("command-v1", []byte(command))
}

func digest(domain string, value []byte) string {
	input := make([]byte, 0, len("nexterm.execbatch.")+len(domain)+1+len(value))
	input = append(input, "nexterm.execbatch."...)
	input = append(input, domain...)
	input = append(input, 0)
	input = append(input, value...)
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}
