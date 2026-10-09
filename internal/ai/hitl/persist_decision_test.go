package hitl

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/adk"
)

func TestAllowPersistentDecisionResumesConfirm(t *testing.T) {
	h := newHarness(t)
	request := h.interrupt(t, "target-one", "call-one", `{"commands":["touch /tmp/x"]}`)
	answer := answerFor("answer-persist", request)
	answer.Decision = DecisionAllowPersist
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); err != nil {
		t.Fatal(err)
	}
	call := h.resumer.call(0)
	if call.params.Targets["target-one"] != "allow_persistent" {
		t.Fatalf("resume value = %v", call.params.Targets)
	}
}

func TestAllowPersistentDecisionRejectedForQuestion(t *testing.T) {
	h := newHarness(t)
	request, err := h.manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target-one", IsRootCause: true}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call-one", Tool: "ask_user", Kind: KindQuestion,
		Parameters: []byte(`{"question":"继续吗？"}`), Question: &Question{Text: "继续吗？"},
	})
	if err != nil {
		t.Fatal(err)
	}
	answer := answerFor("answer-question", request)
	answer.Decision = DecisionAllowPersist
	if _, _, err := h.manager.Resume(context.Background(), h.resumer, answer); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("question resume error = %v", err)
	}
	if _, err := answerValue(KindConfirm, Answer{Decision: DecisionAllowPersist}); err != nil {
		t.Fatalf("confirm answerValue rejected allow_persistent: %v", err)
	}
	if _, err := answerValue(KindQuestion, Answer{Decision: DecisionAllowPersist, Text: "x"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("question answerValue accepted allow_persistent: %v", err)
	}
}
