package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestConversationTitleGenerationPreservesTwoAccountOwners(t *testing.T) {
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(context.Context) (*schema.Message, error) {
			return schema.AssistantMessage("生成标题", nil), nil
		},
	}
	runner, storage := titleTestRunner(t, chat)
	owners := make(chan string, 2)
	runner.config.Model = func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		owners <- ipc.OwnerID(ctx)
		return chat, 32768, nil
	}
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	for _, test := range []struct {
		ctx     context.Context
		message string
		owner   string
	}{{ctxA, "账号 A 的标题", "owner-a"}, {ctxB, "账号 B 的标题", "owner-b"}} {
		conversation, err := storage.ConvCreate(test.ctx, AutoTitle(test.message), map[string]any{"scope": nil})
		if err != nil {
			t.Fatal(err)
		}
		runner.maybeGenerateConversationTitle(&job{args: ChatArgs{ConversationID: conversation.ID, Message: test.message}, ctx: test.ctx})
		waitTitleChange(t, runner, storage, conversation.ID, AutoTitle(test.message))
	}
	seen := map[string]bool{}
	for range 2 {
		seen[<-owners] = true
	}
	if !seen["owner-a"] || !seen["owner-b"] {
		t.Fatalf("title model owners = %v", seen)
	}
}
