package aicontext

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

func TestR1BuilderRejectsMismatchedTabScope(t *testing.T) {
	screenCalls, tailCalls := 0, 0
	builder := NewBuilder(Dependencies{
		TabSession: func(string) string { return "other-session" },
		Screen: func(context.Context, string) (tools.Screen, error) {
			screenCalls++
			return tools.Screen{Text: "secret screen"}, nil
		},
		Tail: func(context.Context, string, int) ([]string, error) {
			tailCalls++
			return []string{"secret tail"}, nil
		},
	})
	prompt := builder.Build(context.Background(), tools.Scope{SessionID: "session", TabID: "foreign-tab"}, "")
	if screenCalls != 0 || tailCalls != 0 {
		t.Fatalf("mismatched tab reads: screen=%d tail=%d", screenCalls, tailCalls)
	}
	if prompt.Bundle.Screen != "" || len(prompt.Bundle.Tail) != 0 {
		t.Fatalf("mismatched tab leaked into prompt: %+v", prompt.Bundle)
	}
}
