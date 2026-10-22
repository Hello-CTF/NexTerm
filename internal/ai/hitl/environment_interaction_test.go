package hitl

import (
	"context"
	"github.com/cloudwego/eino/adk"
	"testing"
	"time"
)

func TestProductionQuestionAnswer(t *testing.T) {
	cp := newFakeCheckpoints()
	cp.put("checkpoint", []byte("checkpoint-one"))
	m, err := NewManager(Config{Checkpoints: cp, TTL: time.Hour, EnvironmentResolver: func(context.Context, string) ([]string, error) { return []string{"production"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err = m.RegisterRun(context.Background(), "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	request, err := m.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "ask_user", Kind: KindQuestion, Parameters: []byte(`{"question":"Continue?"}`), Question: &Question{Text: "Continue?"}})
	if err != nil {
		t.Fatal(err)
	}
	answer := answerFor("answer-one", request)
	answer.Text = "yes"
	answer.Decision = ""
	if _, _, err = m.Resume(context.Background(), &fakeResumer{}, answer); err != nil {
		t.Fatalf("production ask_user answer should resume: %v (required=%v)", err, request.RequiredEnvironmentNames)
	}
}
func TestProductionRejectNoNames(t *testing.T) {
	m, _, _, request := environmentChallengeHarness(t, []string{"production"})
	answer := answerFor("answer-one", request)
	answer.Decision = DecisionDeny
	if _, _, err := m.Resume(context.Background(), &fakeResumer{}, answer); err != nil {
		t.Fatalf("deny should resume without production approval: %v", err)
	}
}
