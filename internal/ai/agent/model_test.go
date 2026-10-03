package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fakeStep struct {
	message *schema.Message
	chunks  []*schema.Message
	err     error
}

type fakeModel struct {
	mu     sync.Mutex
	steps  []fakeStep
	index  int
	stream func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

func sequenceModel(messages ...*schema.Message) *fakeModel {
	steps := make([]fakeStep, len(messages))
	for i, message := range messages {
		steps[i] = fakeStep{message: message}
	}
	return &fakeModel{steps: steps}
}

func (f *fakeModel) next(ctx context.Context) (fakeStep, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fakeStep{}, err
	}
	if len(f.steps) == 0 {
		return fakeStep{message: schema.AssistantMessage("ok", nil)}, nil
	}
	index := f.index
	if index >= len(f.steps) {
		index = len(f.steps) - 1
	} else {
		f.index++
	}
	return f.steps[index], nil
}

func cloneFakeMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	var cloned schema.Message
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(err)
	}
	return &cloned
}

func (f *fakeModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	step, err := f.next(ctx)
	if err != nil {
		return nil, err
	}
	return cloneFakeMessage(step.message), step.err
}

func (f *fakeModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if f.stream != nil {
		return f.stream(ctx, messages, options...)
	}
	step, err := f.next(ctx)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	chunks := step.chunks
	if len(chunks) == 0 && step.message != nil {
		chunks = []*schema.Message{step.message}
	}
	cloned := make([]*schema.Message, len(chunks))
	for index, chunk := range chunks {
		cloned[index] = cloneFakeMessage(chunk)
	}
	return schema.StreamReaderFromArray(cloned), nil
}

func assistantWithUsage(content string, prompt, completion int) *schema.Message {
	message := schema.AssistantMessage(content, nil)
	message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: prompt, CompletionTokens: completion}}
	return message
}

func toolCallMessage(calls ...schema.ToolCall) *schema.Message {
	return schema.AssistantMessage("", calls)
}

func namedToolCall(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}
}
