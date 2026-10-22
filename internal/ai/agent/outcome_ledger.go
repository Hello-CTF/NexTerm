package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// OutcomeState is the lifecycle of one recorded mutation attempt. Initiated
// is in-progress; Succeeded and Failed are final. Unknown (no record) is
// distinct from both and never faked as a final state.
type OutcomeState string

const (
	OutcomeStateInitiated OutcomeState = "initiated"
	OutcomeStateSucceeded OutcomeState = "succeeded"
	OutcomeStateFailed    OutcomeState = "failed"
)

// OutcomeStateFinal reports whether the state is terminal.
func OutcomeStateFinal(state OutcomeState) bool {
	return state == OutcomeStateSucceeded || state == OutcomeStateFailed
}

// OutcomeResidual names what an uncertain mutation left behind after a
// partial failure, so a later retry or cleanup can address it instead of
// hiding it.
type OutcomeResidual struct {
	SessionID string `json:"sessionId,omitempty"`
	Resource  string `json:"resource,omitempty"`
}

// OutcomeRecord is the consumer-facing outcome of one mutation attempt.
type OutcomeRecord struct {
	OperationID  string          `json:"operationId"`
	Kind         string          `json:"kind"`
	State        OutcomeState    `json:"state"`
	FailureStage string          `json:"failureStage,omitempty"`
	Residual     OutcomeResidual `json:"residual"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    int64           `json:"createdAt"`
	UpdatedAt    int64           `json:"updatedAt"`
	FinishedAt   *int64          `json:"finishedAt"`
}

// ErrOutcomeNotFound reports an operation with no recorded outcome: the
// caller cannot treat it as failed or final, only as unknown.
var ErrOutcomeNotFound = errors.New("outcome 不存在")

// OutcomeStore is the persistence contract of the outcome ledger, satisfied
// by *store.Store.
type OutcomeStore interface {
	AgentOutcomeBegin(ctx context.Context, row store.AgentOutcomeRow) (store.AgentOutcomeRow, bool, error)
	AgentOutcomeComplete(ctx context.Context, operationID string, updates map[string]any) (store.AgentOutcomeRow, error)
	AgentOutcomeGet(ctx context.Context, operationID string) (store.AgentOutcomeRow, error)
	AgentOutcomeListByPrefix(ctx context.Context, prefix string, limit int) ([]store.AgentOutcomeRow, error)
}

// OutcomeLedger is the durable, owner-scoped outcome ledger for uncertain
// agent mutations. It records that a mutation was initiated and how it
// finished, so a caller can fetch the original operation's result before
// deciding to retry. It never claims exactly-once execution: the ledger
// anchors identity and outcome, it does not deduplicate work.
type OutcomeLedger struct {
	store OutcomeStore
	now   func() int64
}

func NewOutcomeLedger(store OutcomeStore) *OutcomeLedger {
	return &OutcomeLedger{store: store, now: ids.NowMS}
}

// Begin records that the mutation was initiated. Beginning an operation
// twice returns the original record unchanged.
func (l *OutcomeLedger) Begin(ctx context.Context, operationID, kind string) (OutcomeRecord, error) {
	if strings.TrimSpace(operationID) == "" {
		return OutcomeRecord{}, ipc.NewError(ipc.CodeBadParam, "outcome operation_id 不能为空")
	}
	row, _, err := l.store.AgentOutcomeBegin(ctx, store.AgentOutcomeRow{
		OperationID: operationID, Kind: kind, State: string(OutcomeStateInitiated),
	})
	if err != nil {
		return OutcomeRecord{}, mapOutcomeStoreError(err)
	}
	return outcomeRecordFromRow(row), nil
}

// Succeed records the mutation's successful result. The first final write
// wins; a later completion returns the original outcome unchanged.
func (l *OutcomeLedger) Succeed(ctx context.Context, operationID string, result any) (OutcomeRecord, error) {
	resultJSON, err := marshalOutcomeResult(result)
	if err != nil {
		return OutcomeRecord{}, err
	}
	row, err := l.store.AgentOutcomeComplete(ctx, operationID, map[string]any{
		"state": string(OutcomeStateSucceeded), "result_json": resultJSON, "finished_at": l.now(),
	})
	if err != nil {
		return OutcomeRecord{}, mapOutcomeStoreError(err)
	}
	return outcomeRecordFromRow(row), nil
}

// Fail records the failed mutation with its failure stage and any residual
// session or resource. The first final write wins.
func (l *OutcomeLedger) Fail(ctx context.Context, operationID, stage string, resultErr error, residual OutcomeResidual) (OutcomeRecord, error) {
	message := ""
	if resultErr != nil {
		message = resultErr.Error()
	}
	row, err := l.store.AgentOutcomeComplete(ctx, operationID, map[string]any{
		"state": string(OutcomeStateFailed), "failure_stage": stage, "error": message,
		"residual_session_id": residual.SessionID, "residual_resource": residual.Resource,
		"finished_at": l.now(),
	})
	if err != nil {
		return OutcomeRecord{}, mapOutcomeStoreError(err)
	}
	return outcomeRecordFromRow(row), nil
}

// Result returns the recorded outcome of the operation: an unknown
// operation reports ErrOutcomeNotFound, an unfinished one reports
// OutcomeStateInitiated, and a finished one returns the original result,
// the pre-retry answer.
func (l *OutcomeLedger) Result(ctx context.Context, operationID string) (OutcomeRecord, error) {
	row, err := l.store.AgentOutcomeGet(ctx, operationID)
	if err != nil {
		return OutcomeRecord{}, mapOutcomeStoreError(err)
	}
	return outcomeRecordFromRow(row), nil
}

// OutcomeAdjudication classifies what the durable ledger already holds
// under one operation prefix, so a retry of an uncertain mutation can be
// adjudicated before any new side effect (for example before dialing a
// connection): an existing terminal outcome means the intent already
// reached a final state, a pending one means an earlier attempt never
// finished and its residual must be reconciled, and a conflict means the
// prefix cannot be certified safe to re-attempt.
type OutcomeAdjudication struct {
	// Pending is the newest unfinished (initiated) attempt under the
	// prefix, nil when every recorded attempt has finished.
	Pending *OutcomeRecord
	// Terminal is the newest finished attempt under the prefix, nil when
	// no attempt has finished yet.
	Terminal *OutcomeRecord
	// Conflict reports that the prefix holds more than one unfinished
	// attempt, or more attempts than the adjudication bound could
	// enumerate: the caller must reconcile explicitly instead of starting
	// another attempt.
	Conflict bool
}

// outcomeAdjudicateBound caps how many prefix matches the adjudication
// classifies; hitting the bound is reported as a conflict so an overflowing
// ledger can never certify a single pending attempt.
const outcomeAdjudicateBound = 64

// Adjudicate answers, from the durable ledger alone, what a retry would
// find under the operation prefix: the newest pending attempt, the newest
// terminal outcome, and whether the state is conflicted. Because the
// ledger is persistent and owner-scoped, the answer survives restarts and
// is isolated per owner. The ledger classifies durable records only: the
// caller must still bind its new attempt via Begin and complete it, and
// adjudication before the side effect — not this query — is what prevents
// duplicate work.
func (l *OutcomeLedger) Adjudicate(ctx context.Context, prefix string) (OutcomeAdjudication, error) {
	rows, err := l.store.AgentOutcomeListByPrefix(ctx, prefix, outcomeAdjudicateBound+1)
	if err != nil {
		return OutcomeAdjudication{}, mapOutcomeStoreError(err)
	}
	var adjudication OutcomeAdjudication
	if len(rows) > outcomeAdjudicateBound {
		adjudication.Conflict = true
		rows = rows[:outcomeAdjudicateBound]
	}
	pending := 0
	for i := range rows {
		record := outcomeRecordFromRow(rows[i])
		switch {
		case record.State == OutcomeStateInitiated:
			pending++
			if adjudication.Pending == nil {
				adjudication.Pending = &record
			}
		case adjudication.Terminal == nil:
			adjudication.Terminal = &record
		}
	}
	if pending > 1 {
		adjudication.Conflict = true
	}
	return adjudication, nil
}

func marshalOutcomeResult(result any) (string, error) {
	if result == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", ipc.WrapError(ipc.CodeBadParam, "outcome 结果编码失败: "+err.Error(), err)
	}
	return string(encoded), nil
}

func outcomeRecordFromRow(row store.AgentOutcomeRow) OutcomeRecord {
	record := OutcomeRecord{
		OperationID: row.OperationID, Kind: row.Kind, State: OutcomeState(row.State),
		FailureStage: row.FailureStage,
		Residual:     OutcomeResidual{SessionID: row.ResidualSessionID, Resource: row.ResidualResource},
		Result:       json.RawMessage(row.ResultJSON),
		Error:        row.Error, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, FinishedAt: row.FinishedAt,
	}
	if record.Result == nil {
		record.Result = json.RawMessage("{}")
	}
	return record
}

func mapOutcomeStoreError(err error) error {
	var appErr *ipc.Error
	if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
		return ipc.WrapError(ipc.CodeNotFound, ErrOutcomeNotFound.Error(), ErrOutcomeNotFound)
	}
	return err
}
