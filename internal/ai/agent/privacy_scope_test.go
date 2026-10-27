package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestPrivacyScopeReachesConversationRunAndNextTurn(t *testing.T) {
	storage, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	storage.SetAIHistoryShared(false)
	ctx := privacy.WithConversation(ipc.WithUserID(t.Context(), "owner-a"), "untrusted-context")
	seen := make(chan string, 8)
	chat := sequenceModel(schema.AssistantMessage("first reply", nil), schema.AssistantMessage("second reply", nil))
	runner := NewRunner(Config{Store: storage, ServerMode: true, Tools: tools.NewRegistry(tools.Dependencies{}), Model: func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		if ipc.OwnerID(ctx) != "owner-a" {
			t.Error("conversation model lost account scope")
		}
		seen <- privacy.ConversationID(ctx)
		return chat, 32768, nil
	}})
	t.Cleanup(func() { _ = runner.Close() })
	// A manually named conversation avoids unrelated automatic title requests.
	row, err := storage.ConvCreate(ctx, "Manual title", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first request", "second request"} {
		stream := &SliceStream{}
		_, err := runner.Start(ctx, ChatArgs{ConversationID: row.ID, Message: text}, StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		waitClosed(t, stream)
		select {
		case got := <-seen:
			if got != row.ID {
				t.Fatalf("model scope = %q, want %q", got, row.ID)
			}
		default:
			t.Fatal("model was not called")
		}
	}
}

func TestPrivacyScopeReachesCompactAndRetitle(t *testing.T) {
	runner, storage, _ := compactTestRunner(t, "summary")
	conversationID := seedConversation(t, storage, 2)
	var scopes []string
	chat := &titleTestModel{generate: func(ctx context.Context) (*schema.Message, error) {
		scopes = append(scopes, privacy.ConversationID(ctx))
		return schema.AssistantMessage("summary", nil), nil
	}}
	runner.config.Model = func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil }
	if _, err := runner.CompactConversation(t.Context(), conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RetitleConversation(t.Context(), conversationID); err != nil {
		t.Fatal(err)
	}
	if len(scopes) < 2 {
		t.Fatalf("scope observations = %v", scopes)
	}
	for _, got := range scopes {
		if got != conversationID {
			t.Fatalf("model scope = %q", got)
		}
	}
}
