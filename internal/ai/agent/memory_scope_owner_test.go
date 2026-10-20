package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func memoryTestOwnerScope(ctx context.Context) (memory.Scope, error) {
	if owner.OpenAccess(ctx) {
		return memory.Scope{Tenant: "local", Subject: "default"}, nil
	}
	ownerID, err := owner.ID(ctx, false)
	if err != nil {
		return memory.Scope{}, err
	}
	return memory.Scope{Tenant: "user", Subject: ownerID}, nil
}

func newOwnerMemoryRunner(t *testing.T, chat model.BaseChatModel, memoryStore *memory.Store) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage,
		Memory: memoryStore, MemoryScopeFor: memoryTestOwnerScope, ServerMode: true,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func TestMemoryPromptInjectionUsesConversationOwner(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	scopeA, _ := memoryTestOwnerScope(ctxA)
	scopeB, _ := memoryTestOwnerScope(ctxB)
	memoryStore := openRunnerMemory(t)
	if _, err := memoryStore.Create(context.Background(), scopeA, memory.CreateInput{Topic: "ops", Content: "alpha-only-memory"}); err != nil {
		t.Fatal(err)
	}
	if _, err := memoryStore.Create(context.Background(), scopeB, memory.CreateInput{Topic: "ops", Content: "bravo-only-memory"}); err != nil {
		t.Fatal(err)
	}
	chat := &captureModel{fakeModel: sequenceModel(schema.AssistantMessage("ok", nil))}
	runner, _ := newOwnerMemoryRunner(t, chat, memoryStore)

	for _, test := range []struct {
		ctx       context.Context
		ownerID   string
		present   string
		forbidden string
	}{
		{ctx: ctxA, ownerID: "owner-a", present: "alpha-only-memory", forbidden: "bravo-only-memory"},
		{ctx: owner.AccountContext(context.Background(), "owner-b"), ownerID: "owner-b", present: "bravo-only-memory", forbidden: "alpha-only-memory"},
	} {
		stream := &SliceStream{}
		response, err := runner.Start(test.ctx, ChatArgs{Message: "question-" + test.ownerID}, StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range waitClosed(t, stream) {
			if event.Type == "error" {
				t.Fatalf("run for %s failed: %+v", test.ownerID, event)
			}
		}
		inputs := chat.inputs()
		latest := inputs[len(inputs)-1]
		var payload strings.Builder
		for _, message := range latest {
			payload.WriteString(message.Content)
		}
		if !strings.Contains(payload.String(), test.present) || strings.Contains(payload.String(), test.forbidden) {
			t.Fatalf("model input for %s contains incorrect memory: %s", test.ownerID, payload.String())
		}
		backgroundStream := &SliceStream{}
		backgroundCtx := owner.AccountContext(context.Background(), test.ownerID)
		if _, err := runner.Start(backgroundCtx, ChatArgs{ConversationID: response.ConversationID, Message: "background-" + test.ownerID}, StaticStream(backgroundStream)); err != nil {
			t.Fatal(err)
		}
		for _, event := range waitClosed(t, backgroundStream) {
			if event.Type == "error" {
				t.Fatalf("background run for %s failed: %+v", test.ownerID, event)
			}
		}
		inputs = chat.inputs()
		payload.Reset()
		for _, message := range inputs[len(inputs)-1] {
			payload.WriteString(message.Content)
		}
		if !strings.Contains(payload.String(), test.present) || strings.Contains(payload.String(), test.forbidden) {
			t.Fatalf("background input for %s contains incorrect memory: %s", test.ownerID, payload.String())
		}
	}
}

func TestMemoryToolsWriteOnlyToExecutionOwner(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	scopeA, _ := memoryTestOwnerScope(ctxA)
	scopeB := memory.Scope{Tenant: "user", Subject: "owner-b"}
	memoryStore := openRunnerMemory(t)
	chat := &captureModel{fakeModel: sequenceModel(
		toolCallMessage(namedToolCall("call-owner-memory", memorySaveTool, `{"topic":"ops","content":"owner-a-tool-write"}`)),
		schema.AssistantMessage("saved", nil),
	)}
	runner, _ := newOwnerMemoryRunner(t, chat, memoryStore)
	stream := &SliceStream{}
	if _, err := runner.Start(ctxA, ChatArgs{Message: "remember"}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	for _, event := range waitClosed(t, stream) {
		if event.Type == "error" || (event.Type == "toolResult" && event.ID == "call-owner-memory" && !event.OK) {
			t.Fatalf("memory tool run failed: %+v", event)
		}
	}
	indexA, err := memoryStore.Index(context.Background(), scopeA)
	if err != nil || len(indexA) != 1 || len(indexA[0].Entries) != 1 {
		t.Fatalf("owner A index = %+v, err = %v", indexA, err)
	}
	entry, err := memoryStore.Get(context.Background(), scopeA, indexA[0].Entries[0].ID)
	if err != nil || entry.Content != "owner-a-tool-write" {
		t.Fatalf("owner A tool entry = %+v, err = %v", entry, err)
	}
	if _, err := memoryStore.Get(context.Background(), scopeB, entry.ID); err == nil {
		t.Fatal("memory tool wrote into another owner's scope")
	}
}

func TestMemoryExecutionWithoutAccountFailsClosed(t *testing.T) {
	memoryStore := openRunnerMemory(t)
	chat := &captureModel{fakeModel: sequenceModel(schema.AssistantMessage("ok", nil))}
	runner, _ := newOwnerMemoryRunner(t, chat, memoryStore)
	if _, err := runner.Start(context.Background(), ChatArgs{Message: "no owner"}, StaticStream(&SliceStream{})); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("ownerless account run error = %v", err)
	}
	if _, err := runner.memoryTools(context.Background(), false); err == nil || ipc.NormalizeError(err).Code != ipc.CodeForbidden {
		t.Fatalf("ownerless memory tools error = %v", err)
	}
	if len(chat.inputs()) != 0 {
		t.Fatal("ownerless account run reached the model")
	}
}
