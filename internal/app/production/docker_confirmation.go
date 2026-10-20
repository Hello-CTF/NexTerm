package production

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const dockerProductionConfirmationKind = "docker_production_confirmation"

type dockerProductionConfirmation struct {
	Confirmed     bool   `json:"confirmed"`
	TargetSetHash string `json:"targetSetHash"`
	CommandHash   string `json:"commandHash"`
}

type dockerConfirmationTarget struct {
	SessionID   string `json:"sessionId"`
	SessionName string `json:"sessionName"`
	AssetID     string `json:"assetId,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Environment string `json:"environment"`
}

type dockerMutationOperation struct {
	Kind            string `json:"kind"`
	Project         string `json:"project,omitempty"`
	Path            string `json:"path,omitempty"`
	BaseContentHash string `json:"baseContentHash,omitempty"`
	ContentHash     string `json:"contentHash,omitempty"`
	PreviewToken    string `json:"previewToken,omitempty"`
	PreviewDigest   string `json:"previewDigest,omitempty"`
}

type dockerConfirmationAuditOperation struct {
	Kind            string `json:"kind"`
	Project         string `json:"project,omitempty"`
	Path            string `json:"path,omitempty"`
	BaseContentHash string `json:"baseContentHash,omitempty"`
	ContentHash     string `json:"contentHash,omitempty"`
}

type dockerConfirmationRequiredDetail struct {
	Kind              string                     `json:"kind"`
	TargetSetHash     string                     `json:"targetSetHash"`
	CommandHash       string                     `json:"commandHash"`
	ProductionTargets []dockerConfirmationTarget `json:"productionTargets"`
}

type dockerConfirmationRequiredError struct {
	targetSetHash string
	commandHash   string
	target        dockerConfirmationTarget
}

func (e *dockerConfirmationRequiredError) Error() string {
	return "docker production confirmation required"
}

func (e *dockerConfirmationRequiredError) ipcDetail() dockerConfirmationRequiredDetail {
	return dockerConfirmationRequiredDetail{
		Kind: dockerProductionConfirmationKind, TargetSetHash: e.targetSetHash, CommandHash: e.commandHash,
		ProductionTargets: []dockerConfirmationTarget{e.target},
	}
}

type dockerProductionChallenge struct {
	targetSetHash string
	commandHash   string
	target        dockerConfirmationTarget
}

func newDockerProductionChallenge(target dockerConfirmationTarget, operation dockerMutationOperation) *dockerProductionChallenge {
	if target.Environment != "production" {
		return nil
	}
	targetJSON, _ := json.Marshal(target)
	operationJSON, _ := json.Marshal(operation)
	return &dockerProductionChallenge{
		targetSetHash: dockerConfirmationDigest("target-v1", targetJSON),
		commandHash:   dockerConfirmationDigest("operation-v1", operationJSON), target: target,
	}
}

func validateDockerProductionConfirmation(confirmation *dockerProductionConfirmation, challenge *dockerProductionChallenge) error {
	if challenge == nil {
		return nil
	}
	if confirmation != nil && confirmation.Confirmed && confirmation.TargetSetHash == challenge.targetSetHash && confirmation.CommandHash == challenge.commandHash {
		return nil
	}
	return &dockerConfirmationRequiredError{targetSetHash: challenge.targetSetHash, commandHash: challenge.commandHash, target: challenge.target}
}

func (s *terminalCommandService) resolveDockerConfirmationTarget(ctx context.Context, connected *session.Session) (dockerConfirmationTarget, error) {
	info := connected.Info()
	target := dockerConfirmationTarget{SessionID: info.ID, SessionName: info.Name, AssetID: info.AssetID}
	if info.AssetID == "" || !connected.Asset().StoreBacked {
		return target, nil
	}
	if s.database == nil {
		return dockerConfirmationTarget{}, errors.New("resolve docker asset environment: asset reader unavailable")
	}
	asset, err := s.database.AssetGetLive(productionSessionRequestContext(ctx), info.AssetID)
	if err != nil {
		return dockerConfirmationTarget{}, fmt.Errorf("resolve docker asset environment: %w", err)
	}
	target.Environment = store.AssetEnvironment(asset)
	if asset.Host != nil {
		target.Hostname = *asset.Host
	}
	return target, nil
}

func (s *terminalCommandService) guardDockerMutation(ctx context.Context, connected *session.Session, operation dockerMutationOperation, confirmation *dockerProductionConfirmation) error {
	target, err := s.resolveDockerConfirmationTarget(ctx, connected)
	if err != nil {
		return err
	}
	challenge := newDockerProductionChallenge(target, operation)
	if err := validateDockerProductionConfirmation(confirmation, challenge); err != nil {
		return err
	}
	if challenge == nil {
		return nil
	}
	if s.database == nil {
		return errors.New("record docker production confirmation: audit unavailable")
	}
	auditCtx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(ctx), ipc.OwnerID(ctx)), 5*time.Second)
	defer cancel()
	return s.database.AuditInsert(auditCtx, newDockerConfirmationAuditInput(ctx, target, challenge, operation, time.Now().UnixMilli()))
}

func newDockerConfirmationAuditInput(ctx context.Context, target dockerConfirmationTarget, challenge *dockerProductionChallenge, operation dockerMutationOperation, confirmedAt int64) store.AuditInput {
	auditOperation := dockerConfirmationAuditOperation{
		Kind: operation.Kind, Project: operation.Project, Path: operation.Path,
		BaseContentHash: operation.BaseContentHash, ContentHash: operation.ContentHash,
	}
	return store.AuditInput{
		SessionID: dockerStringPointer(target.SessionID), AssetID: dockerStringPointer(target.AssetID),
		Source: "user", Kind: dockerProductionConfirmationKind,
		Payload: map[string]any{
			"ownerId": ipc.OwnerID(ctx), "targetSetHash": challenge.targetSetHash, "commandHash": challenge.commandHash,
			"operation": auditOperation, "confirmedAt": confirmedAt, "productionTargets": []dockerConfirmationTarget{target},
		},
	}
}

func dockerMutationIPCError(err error) error {
	if err == nil {
		return nil
	}
	var confirmation *dockerConfirmationRequiredError
	if errors.As(err, &confirmation) {
		return ipc.WrapError(ipc.CodeNeedsConfirm, "生产环境 Docker 操作需要显式确认", err).WithDetail(confirmation.ipcDetail())
	}
	return terminalIPCError(err)
}

func dockerConfirmationDigest(domain string, value []byte) string {
	input := make([]byte, 0, len("nexterm.docker.")+len(domain)+1+len(value))
	input = append(input, "nexterm.docker."...)
	input = append(input, domain...)
	input = append(input, 0)
	input = append(input, value...)
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

func dockerStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
