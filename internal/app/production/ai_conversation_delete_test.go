package production

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type fakeConversationRunGuard struct {
	busy    bool
	guarded []string
}

func (g *fakeConversationRunGuard) DeleteConversation(ctx context.Context, conversationID string, remove func(context.Context) error) error {
	g.guarded = append(g.guarded, conversationID)
	if g.busy {
		return agent.ErrConversationDeleteBusy
	}
	return remove(ctx)
}

func TestAIConversationDeleteGoesThroughRunGuard(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := database.ConvCreate(ctx, "t", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	guard := &fakeConversationRunGuard{busy: true}
	dispatcher := ipc.NewDispatcher()
	if err := registerAICommandsGuarded(dispatcher, profileManager, database, guard); err != nil {
		t.Fatal(err)
	}
	args := `{"id":"` + conversation.ID + `"}`

	refused := dispatchStoreTest(dispatcher, "ai_conversation_delete", args)
	if refused.OK || refused.Error == nil || refused.Error.Code != agent.CodeConversationBusy {
		t.Fatalf("delete during a run = %+v, want a conversation_busy error", refused)
	}
	if _, err := database.ConvGet(ctx, conversation.ID); err != nil {
		t.Fatalf("refused delete removed the conversation: %v", err)
	}

	guard.busy = false
	if response := dispatchStoreTest(dispatcher, "ai_conversation_delete", args); !response.OK {
		t.Fatalf("delete after the run = %+v", response)
	}
	if _, err := database.ConvGet(ctx, conversation.ID); err == nil {
		t.Fatal("conversation still exists after delete")
	}
	if len(guard.guarded) != 2 {
		t.Fatalf("guard consulted %d times, want every delete", len(guard.guarded))
	}
}
