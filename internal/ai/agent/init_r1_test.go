package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
)

func TestR1InitializationFailurePersistsUserMessage(t *testing.T) {
	for _, failure := range []string{"permission", "model", "nil-model", "nil-tools"} {
		t.Run(failure, func(t *testing.T) {
			runner, _ := testRunner(t, &fakeModel{}, tools.Dependencies{}, 0)
			switch failure {
			case "permission":
				runner.config.Permission = func(context.Context) (guard.Config, error) {
					return guard.Config{}, errors.New("permission unavailable")
				}
			case "model":
				runner.config.Model = func(context.Context) (model.BaseChatModel, uint64, error) {
					return nil, 0, errors.New("model unavailable")
				}
			case "nil-model":
				runner.config.Model = nil
			case "nil-tools":
				runner.config.Tools = nil
			}
			stream := &SliceStream{}
			response := startTestJob(t, runner, stream, "preserve this request")
			events := waitClosed(t, stream)
			_, failed := terminalCounts(events)
			if failed != 1 {
				t.Fatalf("events = %+v", events)
			}
			messages, err := runner.Messages(context.Background(), response.ConversationID)
			if err != nil || len(messages) != 1 || messages[0].Role != "user" {
				t.Fatalf("messages = %+v, err = %v", messages, err)
			}
			content, ok := messages[0].Content.(map[string]any)
			if !ok || content["content"] != "preserve this request" {
				t.Fatalf("user content = %#v", messages[0].Content)
			}
		})
	}
}
