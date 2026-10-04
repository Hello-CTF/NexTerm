package tools

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestWriteSubagentRunReportsStoreFailure(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	execution := &Execution{Subagents: &SubagentConfig{Runs: database, ConversationID: "conv-1"}}
	err = execution.writeSubagentRun(ctx, subagent.Request{ModelProfileID: "profile-1"}, subagent.Result{
		Status: subagent.StatusCompleted, Turns: 1, Usage: usage.Usage{PromptTokens: 5, CompletionTokens: 2},
	})
	if err == nil {
		t.Fatal("expected an honest write error on a closed store")
	}
}

func TestWriteSubagentRunSkipsWithoutStore(t *testing.T) {
	execution := &Execution{}
	if err := execution.writeSubagentRun(context.Background(), subagent.Request{}, subagent.Result{}); err != nil {
		t.Fatalf("write without store must be a no-op, got %v", err)
	}
}
