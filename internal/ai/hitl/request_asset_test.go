package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

func TestRequestAssetBindingRestoresAndRevalidatesPersistedTarget(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	storage := newFakeRunStore()
	currentAsset := "production-asset"
	config := Config{
		Checkpoints: checkpoints, Store: storage, TTL: time.Hour,
		AssetResolver: func(context.Context, string, string) (string, error) { return "scope-asset", nil },
		RequestAssetResolver: func(_ context.Context, runID, tool string, parameters json.RawMessage) (string, error) {
			var args struct {
				TabID string `json:"tab_id"`
			}
			if err := json.Unmarshal(parameters, &args); err != nil {
				return "", err
			}
			if runID != "run" || tool != "send_keys" || args.TabID != "created-tab" {
				return "", errors.New("persisted request target changed")
			}
			return currentAsset, nil
		},
	}
	first, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(t.Context(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(t.Context(), &adk.InterruptCtx{ID: "target", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call", Tool: "send_keys", Kind: KindConfirm,
		Parameters: json.RawMessage(`{"tab_id":"created-tab","keys":"touch target","enter":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.AssetID != "production-asset" {
		t.Fatalf("request bound to %q instead of actual target", request.AssetID)
	}
	blob := storage.snapshot("run")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if err := restored.Restore(blob); err != nil {
		t.Fatal(err)
	}
	currentAsset = "replacement-asset"
	if _, _, err := restored.Resume(t.Context(), &fakeResumer{}, answerFor("changed-answer", request)); !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("changed target resume = %v", err)
	}
	currentAsset = "production-asset"
	resume, iterator, err := restored.Resume(t.Context(), &fakeResumer{}, answerFor("valid-answer", request))
	if err != nil || iterator == nil || resume.AssetID != "production-asset" {
		t.Fatalf("restored target resume = %+v err=%v", resume, err)
	}
}
