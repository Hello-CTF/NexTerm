package agent

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

func TestConversationMemorySpansRunsButNotTargets(t *testing.T) {
	runner := &Runner{}
	scope := tools.Scope{AssetID: "asset-a", SessionID: "s1", TabID: "t1"}
	first := runner.conversationMemory("owner", "conv", scope)
	if again := runner.conversationMemory("owner", "conv", scope); again != first {
		t.Fatal("a later run of the same conversation and target must share approvals")
	}
	for name, other := range map[string]*struct {
		owner, conversation string
		scope               tools.Scope
	}{
		"other host":         {"owner", "conv", tools.Scope{AssetID: "asset-b", SessionID: "s1", TabID: "t1"}},
		"other session":      {"owner", "conv", tools.Scope{AssetID: "asset-a", SessionID: "s2", TabID: "t1"}},
		"other conversation": {"owner", "conv-2", scope},
		"other owner":        {"owner-2", "conv", scope},
	} {
		if runner.conversationMemory(other.owner, other.conversation, other.scope) == first {
			t.Fatalf("%s shares the approvals of the first target", name)
		}
	}
	if runner.conversationMemory("owner", "", scope) == runner.conversationMemory("owner", "", scope) {
		t.Fatal("runs without a conversation must not share approvals")
	}
}
