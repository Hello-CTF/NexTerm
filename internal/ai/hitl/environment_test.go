package hitl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

// environmentChallengeHarness builds a manager whose environment resolver
// reports the given required names for the run's target asset.
func environmentChallengeHarness(t *testing.T, requiredNames []string) (*Manager, *fakeRunStore, *fakeCheckpoints, Interrupt) {
	t.Helper()
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	manager, err := NewManager(Config{
		Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour,
		EnvironmentResolver: func(context.Context, string) ([]string, error) {
			return append([]string(nil), requiredNames...), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, blobStore, checkpoints, request
}

func TestInterruptCarriesProductionEnvironmentChallenge(t *testing.T) {
	_, blobStore, _, request := environmentChallengeHarness(t, []string{"production"})
	if len(request.RequiredEnvironmentNames) != 1 || request.RequiredEnvironmentNames[0] != "production" {
		t.Fatalf("challenge required names = %v", request.RequiredEnvironmentNames)
	}
	// The challenge survives the persisted round trip.
	blob := blobStore.snapshot("run")
	var record runRecord
	if err := json.Unmarshal(blob.Data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Requests) != 1 || len(record.Requests[0].Interrupt.RequiredEnvironmentNames) != 1 {
		t.Fatalf("persisted challenge = %+v", record.Requests)
	}
}

func TestResumeEnvironmentNamesValidation(t *testing.T) {
	cases := []struct {
		name     string
		required []string
		typed    []string
		wantErr  bool
	}{
		{"matching", []string{"production"}, []string{"production"}, false},
		{"matching case and space insensitive", []string{"production"}, []string{" Production "}, false},
		{"missing", []string{"production"}, nil, true},
		{"wrong", []string{"production"}, []string{"staging"}, true},
		{"extra beyond required", []string{"production"}, []string{"production", "staging"}, true},
		{"extra without challenge", nil, []string{"production"}, true},
		{"empty both", nil, nil, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manager, blobStore, _, request := environmentChallengeHarness(t, testCase.required)
			resumer := &fakeResumer{}
			answer := answerFor("answer-one", request)
			answer.EnvironmentNames = testCase.typed
			before := blobStore.snapshot("run")
			_, _, err := manager.Resume(context.Background(), resumer, answer)
			if testCase.wantErr {
				if !errors.Is(err, ErrEnvironmentNamesMismatch) {
					t.Fatalf("resume error = %v", err)
				}
				// Zero execution: the request stays pending, the resumer is
				// never started, and no state is persisted.
				snapshot, err := manager.Snapshot("run")
				if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].ID != request.ID {
					t.Fatalf("pending after rejected resume = %+v err=%v", snapshot, err)
				}
				resumer.mu.Lock()
				resumeCalls := len(resumer.calls)
				resumer.mu.Unlock()
				if resumeCalls != 0 {
					t.Fatalf("resumer started %d times", resumeCalls)
				}
				if again := blobStore.snapshot("run"); !bytes.Equal(again.Data, before.Data) {
					t.Fatal("blob rewritten by rejected resume")
				}
				return
			}
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			resumer.mu.Lock()
			resumeCalls := len(resumer.calls)
			resumer.mu.Unlock()
			if resumeCalls != 1 {
				t.Fatalf("resumer started %d times, want 1", resumeCalls)
			}
		})
	}
}

func TestResumeEnvironmentNamesAfterRestart(t *testing.T) {
	checkpoints := newFakeCheckpoints()
	checkpoints.put("checkpoint", []byte("checkpoint-one"))
	blobStore := newFakeRunStore()
	resolver := func(context.Context, string) ([]string, error) { return []string{"production"}, nil }
	first, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, EnvironmentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := first.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "exec_commands", Kind: KindConfirm, Parameters: []byte(`{"commands":["ls"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	captured := blobStore.snapshot("run")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewManager(Config{Checkpoints: checkpoints, Store: blobStore, TTL: time.Hour, EnvironmentResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.Restore(captured); err != nil {
		t.Fatal(err)
	}
	// The restored challenge still requires the typed names: a bare
	// confirmation is rejected, the typed one resumes.
	if _, _, err := second.Resume(context.Background(), &fakeResumer{}, answerFor("answer-bare", request)); !errors.Is(err, ErrEnvironmentNamesMismatch) {
		t.Fatalf("restored bare resume error = %v", err)
	}
	answer := answerFor("answer-typed", request)
	answer.EnvironmentNames = []string{"production"}
	resume, iterator, err := second.Resume(context.Background(), &fakeResumer{}, answer)
	if err != nil || iterator == nil || resume.Attempt != 2 {
		t.Fatalf("restored typed resume = %+v err=%v", resume, err)
	}
}
