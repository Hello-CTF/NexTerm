package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// privacyCaptureChat records the conversation redaction choice each model call carries.
type privacyCaptureChat struct {
	mu   sync.Mutex
	seen []provider.PrivacyOverride
}

func (c *privacyCaptureChat) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	c.mu.Lock()
	c.seen = append(c.seen, provider.RequestPrivacy(ctx))
	c.mu.Unlock()
	return schema.AssistantMessage("done", nil), nil
}

func (c *privacyCaptureChat) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := c.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestChatPrivacyChoiceReachesTheModelAndIsSavedWithTheRun(t *testing.T) {
	chat := &privacyCaptureChat{}
	runner, storage := testRunner(t, chat, tools.Dependencies{}, 0)
	runner.runs = storage
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "hi", Scope: tools.Scope{SessionID: "session"}, Privacy: provider.PrivacyOff}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	if done, failed := terminalCounts(waitClosed(t, stream)); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	chat.mu.Lock()
	seen := append([]provider.PrivacyOverride(nil), chat.seen...)
	chat.mu.Unlock()
	if len(seen) == 0 || seen[0] != provider.PrivacyOff {
		t.Fatalf("model calls saw %q, want the conversation's off choice", seen)
	}
	run, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Privacy != string(provider.PrivacyOff) {
		t.Fatalf("run privacy = %q, want off so a restored run keeps it", run.Privacy)
	}
}

func TestChatRejectsAnUnknownPrivacyChoice(t *testing.T) {
	runner, _ := testRunner(t, &privacyCaptureChat{}, tools.Dependencies{}, 0)
	if _, err := runner.Start(context.Background(), ChatArgs{Message: "hi", Scope: tools.Scope{SessionID: "session"}, Privacy: "maybe"}, StaticStream(&SliceStream{})); err == nil {
		t.Fatal("an unknown privacy choice must be refused")
	}
}

// privacyCaptureWrap records each call's redaction choice and answers with the wrapped model.
type privacyCaptureWrap struct {
	privacyCaptureChat
	next model.BaseChatModel
}

func (w *privacyCaptureWrap) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	w.mu.Lock()
	w.seen = append(w.seen, provider.RequestPrivacy(ctx))
	w.mu.Unlock()
	return w.next.Generate(ctx, input, opts...)
}

func (w *privacyCaptureWrap) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := w.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestRestoredRunKeepsTheConversationPrivacyChoice(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}, Privacy: provider.PrivacyOn}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	chat := &privacyCaptureWrap{next: sequenceModel(schema.AssistantMessage("done", nil))}
	restarted := durableRunner(t, storage, chat, deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	if done, failed := terminalCounts(waitClosed(t, resumed)); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(chat.seen) == 0 || chat.seen[0] != provider.PrivacyOn {
		t.Fatalf("restored model calls saw %q, want the conversation's on choice", chat.seen)
	}
}
