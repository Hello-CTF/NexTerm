package takeover

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakeModel struct {
	mu       sync.Mutex
	messages []*schema.Message
	index    int
	stream   func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

func sequence(messages ...*schema.Message) *fakeModel {
	return &fakeModel{messages: messages}
}

func (f *fakeModel) next(ctx context.Context) (*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(f.messages) == 0 {
		return schema.AssistantMessage("ok", nil), nil
	}
	index := f.index
	if index >= len(f.messages) {
		index = len(f.messages) - 1
	} else {
		f.index++
	}
	return f.messages[index], nil
}

func (f *fakeModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return f.next(ctx)
}

func (f *fakeModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if f.stream != nil {
		return f.stream(ctx, messages, options...)
	}
	message, err := f.next(ctx)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func toolCallMessage(calls ...schema.ToolCall) *schema.Message {
	return schema.AssistantMessage("", calls)
}

func namedToolCall(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}
}

func doneCall(summary string) schema.ToolCall {
	return namedToolCall("done", "done", `{"summary":"`+summary+`","success":true}`)
}
