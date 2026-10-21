package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// durableStoreOutcomes is the production DurableOutcomeLedger: it adapts the
// store's owner-scoped agent-outcome primitives, so recorded terminal_create
// operations are adjudicated from durable rows that survive restarts and are
// isolated per owner — never from an in-process substitute. Every method
// delegates to the store without reimplementing its concurrency contracts:
// the claim insert's primary-key race picks the single winner, the progress
// write's compare-and-swap refuses a released or completed claim, and the
// completion transaction writes the history row and releases the claim
// atomically.
type durableStoreOutcomes struct {
	store *store.Store
	now   func() int64
}

// DurableStoreOutcomes exposes the store-backed durable outcome ledger used
// by the production wiring; nil storage keeps the in-process adjudication
// only.
func DurableStoreOutcomes(storage *store.Store) DurableOutcomeLedger {
	if storage == nil {
		return nil
	}
	return &durableStoreOutcomes{store: storage, now: func() int64 { return time.Now().UnixMilli() }}
}

func (d *durableStoreOutcomes) ClaimOutcome(ctx context.Context, family, kind string) (string, bool, error) {
	claimed, err := d.store.AgentOutcomeClaimAttempt(ctx, family, kind)
	return store.AgentOutcomeClaimRowID(family), claimed, err
}

func (d *durableStoreOutcomes) ProgressOutcome(ctx context.Context, family, residualSessionID string) error {
	return d.store.AgentOutcomeProgress(ctx, family, residualSessionID)
}

func (d *durableStoreOutcomes) ReleaseOutcome(ctx context.Context, family string) error {
	return d.store.AgentOutcomeReleaseClaim(ctx, family)
}

func (d *durableStoreOutcomes) CompleteOutcome(ctx context.Context, family string, attempt OutcomeAttempt) error {
	now := d.now()
	return d.store.AgentOutcomeCompleteAttempt(ctx, family, store.AgentOutcomeRow{
		OperationID: attempt.OperationID, Kind: attempt.Kind, State: attempt.State,
		FailureStage:      attempt.FailureStage,
		ResidualSessionID: attempt.ResidualSessionID, ResidualResource: attempt.ResidualResource,
		ResultJSON: string(attempt.Result), Error: attempt.Error, FinishedAt: &now,
	})
}

// durableAdjudicateBound caps how many prefix matches the adjudication
// classifies; hitting the bound is reported as a conflict so an overflowing
// ledger can never certify a single pending attempt.
const durableAdjudicateBound = 64

func (d *durableStoreOutcomes) AdjudicateOutcome(ctx context.Context, prefix string) (OutcomeAdjudication, error) {
	rows, err := d.store.AgentOutcomeListByPrefix(ctx, prefix, durableAdjudicateBound+1)
	if err != nil {
		return OutcomeAdjudication{}, err
	}
	var adjudication OutcomeAdjudication
	if len(rows) > durableAdjudicateBound {
		adjudication.Conflict = true
		rows = rows[:durableAdjudicateBound]
	}
	pending := 0
	for _, row := range rows {
		attempt := outcomeAttemptFromRow(row)
		switch {
		case row.State == store.AgentOutcomeStateInitiated:
			pending++
			if adjudication.Pending == nil {
				adjudication.Pending = &attempt
			}
		case adjudication.Terminal == nil:
			adjudication.Terminal = &attempt
		}
	}
	if pending > 1 {
		adjudication.Conflict = true
	}
	return adjudication, nil
}

// OutcomeResult fetches the operation's recorded outcome by its operation
// ID — the family every attempt row of the operation lives under: history
// rows carry nonce-suffixed IDs and the in-flight claim row the ":claim"
// suffix, so the read enumerates the family prefix newest-first and takes
// the newest row that is the family itself or a ":…" descendant of it.
// That boundary check keeps the prefix read exact: a queried ID can never
// resolve a different operation's record by merely sharing leading
// characters with it.
func (d *durableStoreOutcomes) OutcomeResult(ctx context.Context, operationID string) (OutcomeAttempt, error) {
	rows, err := d.store.AgentOutcomeListByPrefix(ctx, operationID, durableAdjudicateBound)
	if err != nil {
		return OutcomeAttempt{}, err
	}
	for _, row := range rows {
		if row.OperationID == operationID || strings.HasPrefix(row.OperationID, operationID+":") {
			return outcomeAttemptFromRow(row), nil
		}
	}
	return OutcomeAttempt{}, ipc.NewError(ipc.CodeNotFound, "outcome 记录不存在")
}

func outcomeAttemptFromRow(row store.AgentOutcomeRow) OutcomeAttempt {
	attempt := OutcomeAttempt{
		OperationID: row.OperationID, Kind: row.Kind, State: row.State,
		FailureStage:      row.FailureStage,
		ResidualSessionID: row.ResidualSessionID, ResidualResource: row.ResidualResource,
		Result: json.RawMessage(row.ResultJSON), Error: row.Error,
	}
	if attempt.Result == nil {
		attempt.Result = json.RawMessage("{}")
	}
	return attempt
}
