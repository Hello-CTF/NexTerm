package hitl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

type assetResolver struct {
	mu    sync.Mutex
	asset string
	err   error
}

func (r *assetResolver) resolve(context.Context, string, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asset, r.err
}

func (r *assetResolver) set(asset string) {
	r.mu.Lock()
	r.asset = asset
	r.mu.Unlock()
}

func (r *assetResolver) setErr(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func newAssetHarness(t *testing.T, resolver *assetResolver) *harness {
	t.Helper()
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	var nonce atomic.Uint64
	manager, err := NewManager(Config{
		Checkpoints: store,
		TTL:         time.Hour,
		NewNonce: func() (string, error) {
			return fmt.Sprintf("nonce-%d", nonce.Add(1)), nil
		},
		AssetResolver: resolver.resolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	return &harness{manager: manager, store: store, resumer: &fakeResumer{}}
}

func TestInterruptResumeBindsAssetEvidence(t *testing.T) {
	h := newAssetHarness(t, &assetResolver{asset: "asset-a"})
	request := h.interrupt(t, "target-one", "call-one", `{"commands":["ls"]}`)
	if request.AssetID != "asset-a" {
		t.Fatalf("interrupt asset evidence = %q", request.AssetID)
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "asset-a" {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	answer := answerFor("answer-one", request)
	answer.AssetID = "asset-a"
	resume, _, err := h.manager.Resume(context.Background(), h.resumer, answer)
	if err != nil {
		t.Fatal(err)
	}
	if resume.AssetID != "asset-a" {
		t.Fatalf("resume asset evidence = %q", resume.AssetID)
	}
	if _, err := h.manager.Finish("run", TerminalCompleted, nil); err != nil {
		t.Fatal(err)
	}
	events, err := h.manager.Events("run", 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if events[0].Kind != EventInterrupted || events[0].AssetID != "asset-a" {
		t.Fatalf("interrupted event = %+v", events[0])
	}
	if events[1].Kind != EventResumed || events[1].AssetID != "asset-a" {
		t.Fatalf("resumed event = %+v", events[1])
	}
	if events[2].Kind != EventTerminal || events[2].AssetID != "" {
		t.Fatalf("terminal event = %+v", events[2])
	}
}

func TestAnswerAssetClaimCannotOverrideEvidence(t *testing.T) {
	h := newAssetHarness(t, &assetResolver{asset: "asset-a"})
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	answer := answerFor("answer-one", request)
	answer.AssetID = "asset-b"
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("cross-asset claim error = %v", err)
	}
	if h.resumer.count() != 0 {
		t.Fatalf("cross-asset claim resumed %d times", h.resumer.count())
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 {
		t.Fatalf("rejected claim consumed the request: %+v, %v", snapshot, err)
	}
	answer = answerFor("answer-one", request)
	answer.AssetID = "asset-a"
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); err != nil {
		t.Fatalf("matching claim rejected: %v", err)
	}
}

func TestAssetChangeInvalidatesAnswerWithoutConsuming(t *testing.T) {
	resolver := &assetResolver{asset: "asset-a"}
	h := newAssetHarness(t, resolver)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	resolver.set("asset-b")
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request)); !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("changed asset error = %v", err)
	}
	if h.resumer.count() != 0 {
		t.Fatalf("changed asset resumed %d times", h.resumer.count())
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "asset-a" {
		t.Fatalf("pending after mismatch = %+v, %v", snapshot, err)
	}
	resolver.set("asset-a")
	resume, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request))
	if err != nil {
		t.Fatalf("answer after asset restored: %v", err)
	}
	if resume.AssetID != "asset-a" {
		t.Fatalf("resume asset evidence = %q", resume.AssetID)
	}
}

func TestInterruptRefreshesChangedAssetEvidence(t *testing.T) {
	resolver := &assetResolver{asset: "asset-a"}
	h := newAssetHarness(t, resolver)
	first := h.interrupt(t, "target-one", "call-one", `{"commands":["ls"]}`)
	resolver.set("asset-b")
	second, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Nonce == first.Nonce || second.AssetID != "asset-b" || second.Sequence != 2 {
		t.Fatalf("refreshed request = %+v, first = %+v", second, first)
	}
	stale := answerFor("stale-answer", first)
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, stale); !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("stale evidence answer error = %v", err)
	}
	resume, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("fresh-answer", second))
	if err != nil {
		t.Fatal(err)
	}
	if resume.AssetID != "asset-b" {
		t.Fatalf("resume asset evidence = %q", resume.AssetID)
	}
}

func TestAssetResolverFailureFailsClosed(t *testing.T) {
	resolver := &assetResolver{err: errors.New("lookup down")}
	h := newAssetHarness(t, resolver)
	if _, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	}); err == nil || !strings.Contains(err.Error(), "asset evidence") {
		t.Fatalf("resolver failure error = %v", err)
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 0 {
		t.Fatalf("failed resolver created a request: %+v, %v", snapshot, err)
	}
	resolver.setErr(nil)
	if _, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm,
	}); !errors.Is(err, ErrAssetUnknown) {
		t.Fatalf("unknown asset interrupt error = %v", err)
	}
	resolver.set("asset-a")
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	resolver.set("")
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request)); !errors.Is(err, ErrAssetUnknown) {
		t.Fatalf("unknown current asset error = %v", err)
	}
	resolver.setErr(errors.New("lookup down"))
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request)); err == nil || !strings.Contains(err.Error(), "asset evidence") {
		t.Fatalf("resume resolver failure error = %v", err)
	}
	snapshot, err = h.manager.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 {
		t.Fatalf("failed resume consumed the request: %+v, %v", snapshot, err)
	}
}

func TestAssetBindingDisabledWithoutResolver(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	if request.AssetID != "" {
		t.Fatalf("interrupt asset evidence without resolver = %q", request.AssetID)
	}
	resume, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", request))
	if err != nil {
		t.Fatal(err)
	}
	if resume.AssetID != "" {
		t.Fatalf("resume asset evidence without resolver = %q", resume.AssetID)
	}

	other := newHarness(t)
	otherRequest := other.interrupt(t, "target-one", "call-one", `{}`)
	claim := answerFor("answer-one", otherRequest)
	claim.AssetID = "asset-a"
	if _, _, err := other.manager.Resume(context.Background(), other.resumer, claim); !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("claim without evidence error = %v", err)
	}
}

func TestAnswerBoundToOwningRun(t *testing.T) {
	h := newAssetHarness(t, &assetResolver{asset: "asset-a"})
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	h.store.put("checkpoint-two", []byte("checkpoint-two"))
	if _, err := h.manager.RegisterRun(context.Background(), "run-two", "checkpoint-two"); err != nil {
		t.Fatal(err)
	}
	foreign := answerFor("foreign-answer", request)
	foreign.RunID = "run-two"
	foreign.CheckpointID = "checkpoint-two"
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, foreign); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("cross-run answer error = %v", err)
	}
	if h.resumer.count() != 0 {
		t.Fatalf("cross-run answer resumed %d times", h.resumer.count())
	}
}

func TestRestoredRequestKeepsAssetEvidence(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	var nonce atomic.Uint64
	first, err := NewManager(Config{
		Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour,
		NewNonce:      func() (string, error) { return fmt.Sprintf("nonce-%d", nonce.Add(1)), nil },
		AssetResolver: (&assetResolver{asset: "asset-a"}).resolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")

	second, err := NewManager(Config{
		Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour,
		AssetResolver: (&assetResolver{asset: "asset-a"}).resolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "asset-a" {
		t.Fatalf("restored pending = %+v, %v", snapshot, err)
	}
	resume, _, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request))
	if err != nil {
		t.Fatal(err)
	}
	if resume.AssetID != "asset-a" {
		t.Fatalf("restored resume asset evidence = %q", resume.AssetID)
	}
}

func TestRestoredRequestWithoutEvidenceStaysUnknown(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")

	second, err := NewManager(Config{
		Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour,
		AssetResolver: (&assetResolver{asset: "asset-a"}).resolve,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "" {
		t.Fatalf("restored pending = %+v, %v", snapshot, err)
	}
	if _, _, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-one", request)); !errors.Is(err, ErrAssetUnknown) {
		t.Fatalf("no-evidence resume error = %v", err)
	}
}

func TestCancelledRunRejectsAssetBoundAnswer(t *testing.T) {
	h := newAssetHarness(t, &assetResolver{asset: "asset-a"})
	request := h.interrupt(t, "target-one", "call-one", `{}`)
	if _, err := h.manager.Cancel("run"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("late", request)); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("cancelled run answer error = %v", err)
	}
	if h.resumer.count() != 0 {
		t.Fatalf("cancelled run resumed %d times", h.resumer.count())
	}
}

// toolAssetResolver resolves per-tool asset identities, modeling the
// production chain where a database tool binds the connection's real DB asset
// and a terminal tool binds the session asset inside one mixed scope.
type toolAssetResolver struct {
	mu     sync.Mutex
	assets map[string]string
}

func (r *toolAssetResolver) resolve(_ context.Context, _ string, tool string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.assets[tool], nil
}

func newToolAssetHarness(t *testing.T, resolver *toolAssetResolver, environmentResolver func(context.Context, string) ([]string, error)) *harness {
	t.Helper()
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	var nonce atomic.Uint64
	manager, err := NewManager(Config{
		Checkpoints: store,
		TTL:         time.Hour,
		NewNonce: func() (string, error) {
			return fmt.Sprintf("nonce-%d", nonce.Add(1)), nil
		},
		AssetResolver:       resolver.resolve,
		EnvironmentResolver: environmentResolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	return &harness{manager: manager, store: store, resumer: &fakeResumer{}}
}

func (h *harness) interruptTool(t *testing.T, targetID, callID, tool string) Interrupt {
	t.Helper()
	request, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: targetID, IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: callID, Tool: tool, Kind: KindConfirm, Parameters: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// The interrupt and the resume comparison bind to the actual confirming tool:
// a database call carries the connection's real asset and its production
// challenge, a terminal call in the same run carries the session asset, and a
// flipped tool identity rejects the pending answer instead of resuming on
// stale evidence.
func TestInterruptResumeBindsEvidenceToTheConfirmingTool(t *testing.T) {
	resolver := &toolAssetResolver{assets: map[string]string{"db_query": "asset-db", "send_keys": "asset-term"}}
	h := newToolAssetHarness(t, resolver, func(_ context.Context, assetID string) ([]string, error) {
		if assetID == "asset-db" {
			return []string{"live"}, nil
		}
		return nil, nil
	})

	dbRequest := h.interruptTool(t, "target-one", "call-db", "db_query")
	if dbRequest.AssetID != "asset-db" {
		t.Fatalf("db interrupt asset evidence = %q", dbRequest.AssetID)
	}
	if len(dbRequest.RequiredEnvironmentNames) != 1 || dbRequest.RequiredEnvironmentNames[0] != "live" {
		t.Fatalf("db interrupt required names = %v", dbRequest.RequiredEnvironmentNames)
	}
	dbAnswer := answerFor("answer-db", dbRequest)
	dbAnswer.EnvironmentNames = []string{"live"}
	resume, _, err := h.manager.Resume(context.Background(), h.resumer, dbAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if resume.AssetID != "asset-db" {
		t.Fatalf("db resume asset evidence = %q", resume.AssetID)
	}

	termRequest := h.interruptTool(t, "target-one", "call-term", "send_keys")
	if termRequest.AssetID != "asset-term" {
		t.Fatalf("terminal interrupt asset evidence = %q", termRequest.AssetID)
	}
	if len(termRequest.RequiredEnvironmentNames) != 0 {
		t.Fatalf("terminal interrupt required names = %v", termRequest.RequiredEnvironmentNames)
	}
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-term", termRequest)); err != nil {
		t.Fatal(err)
	}

	// The database connection is rebound while the next confirmation is
	// pending: the answer is rejected as mismatched instead of resuming on
	// stale evidence, and the request stays pending for the real asset.
	resolver.mu.Lock()
	resolver.assets["db_query"] = "asset-db-new"
	resolver.mu.Unlock()
	rebound := h.interruptTool(t, "target-one", "call-db-2", "db_query")
	if rebound.AssetID != "asset-db-new" {
		t.Fatalf("rebound interrupt asset evidence = %q", rebound.AssetID)
	}
	resolver.mu.Lock()
	resolver.assets["db_query"] = "asset-db-third"
	resolver.mu.Unlock()
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-db-2", rebound)); !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("rebound resume error = %v", err)
	}
	if h.resumer.count() != 2 {
		t.Fatalf("mismatched answer resumed %d times", h.resumer.count())
	}
	snapshot, err := h.manager.Snapshot("run")
	if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].AssetID != "asset-db-new" {
		t.Fatalf("rejected rebound consumed the request: %+v, %v", snapshot, err)
	}
}
