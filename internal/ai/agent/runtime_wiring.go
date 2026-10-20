package agent

import (
	"context"
	"reflect"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// activeRunPager pages the active run set; *store.Store satisfies it.
type activeRunPager interface {
	RunPage(ctx context.Context, filter store.RunPageFilter) (store.RunPage, error)
}

// conditionalRunFinisher finishes a run under the scanned-status guard;
// *store.Store satisfies it.
type conditionalRunFinisher interface {
	RunFinishUsageIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) (bool, error)
}

// atomicRunFinisher finishes a run and appends its terminal event in one
// atomic expected-status operation; *store.Store satisfies it.
type atomicRunFinisher interface {
	RunFinishWithEventIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64, eventType string, eventPayload func(seq uint64) ([]byte, error)) (bool, error)
}

// nilCapableStore reports whether the store value is a typed nil: an
// interface holding a nil *store.Store passes type assertions but panics
// on method calls, so capability boundaries must treat it as absent.
func nilCapableStore(store any) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

// evidenceStoreOf resolves the evidence capability, rejecting typed-nil
// stores.
func evidenceStoreOf(store ConversationStore) (EvidenceStore, bool) {
	if nilCapableStore(store) {
		return nil, false
	}
	capability, ok := store.(EvidenceStore)
	return capability, ok
}

// outcomeStoreOf resolves the outcome capability, rejecting typed-nil
// stores.
func outcomeStoreOf(store ConversationStore) (OutcomeStore, bool) {
	if nilCapableStore(store) {
		return nil, false
	}
	capability, ok := store.(OutcomeStore)
	return capability, ok
}

// evidenceRecorder returns the recorder-side seal authority when the
// configured store supports evidence persistence, or nil.
func (r *Runner) evidenceRecorder() *EvidenceRecorder {
	store, ok := evidenceStoreOf(r.store)
	if !ok {
		return nil
	}
	return NewEvidenceRecorder(store)
}

// evidenceVerifier returns the review-side authority when the configured
// store supports evidence persistence, or nil.
func (r *Runner) evidenceVerifier() *EvidenceVerifier {
	store, ok := evidenceStoreOf(r.store)
	if !ok {
		return nil
	}
	return NewEvidenceVerifier(store)
}

// outcomeLedger returns the durable outcome ledger when the configured
// store supports it, or nil.
func (r *Runner) outcomeLedger() *OutcomeLedger {
	store, ok := outcomeStoreOf(r.store)
	if !ok {
		return nil
	}
	return NewOutcomeLedger(store)
}

// atomicFinisher returns the atomic event+finish capability, rejecting
// typed-nil stores.
func (r *Runner) atomicFinisher() (atomicRunFinisher, bool) {
	if nilCapableStore(r.runs) {
		return nil, false
	}
	capability, ok := r.runs.(atomicRunFinisher)
	return capability, ok
}

// OutcomeResult returns the recorded outcome of an uncertain mutation, so
// a caller can fetch the original operation's result before deciding to
// retry. It is the real result-query path backed by the durable ledger.
func (r *Runner) OutcomeResult(ctx context.Context, operationID string) (OutcomeRecord, error) {
	ledger := r.outcomeLedger()
	if ledger == nil {
		return OutcomeRecord{}, ipc.NewError(ipc.CodeUnsupported, "outcome ledger 未配置")
	}
	return ledger.Result(ctx, operationID)
}
