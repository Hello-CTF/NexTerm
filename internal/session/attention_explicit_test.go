package session

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestAttentionIgnoresGuessedStatesFromOldHelpers(t *testing.T) {
	since := time.Now()
	for name, state := range map[string]*base.SessionState{
		"prompt reason":      {State: base.SessionNeedsMe, Confidence: base.ConfidenceSignal, Reason: "prompt", Since: since},
		"heuristic":          {State: base.SessionNeedsMe, Confidence: "heuristic", Reason: "prompt", Since: since},
		"heuristic bell":     {State: base.SessionNeedsMe, Confidence: "heuristic", Reason: "bell", Since: since},
		"heuristic finished": {State: base.SessionJustFinished, Confidence: "heuristic", Since: since},
	} {
		event := attentionFromState(state, AttentionSourceHelper)
		if event.State != base.SessionRunning || !event.Seen {
			t.Errorf("%s: got state %q seen %v, want a quiet running state", name, event.State, event.Seen)
		}
	}
	for _, reason := range []string{"bell", "notify", "rule"} {
		confidence := base.ConfidenceSignal
		if reason == "rule" {
			confidence = base.ConfidenceRule
		}
		event := attentionFromState(&base.SessionState{State: base.SessionNeedsMe, Confidence: confidence, Reason: reason, Since: since}, AttentionSourceHelper)
		if event.State != base.SessionNeedsMe || event.Seen {
			t.Errorf("%s: explicit state was changed to %q seen %v", reason, event.State, event.Seen)
		}
	}
}
