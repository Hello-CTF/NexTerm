package provider

import (
	"context"
)

func (c *Client) Chat(ctx context.Context, request ChatRequest, handler StreamHandler) (Completion, error) {
	messages, options, err := toNativeRequest(request)
	if err != nil {
		return Completion{}, err
	}
	result, err := c.runNative(ctx, messages, options, request.ResponseFormat, c.config.Stream, nil, handler)
	if err != nil {
		return result.completion, err
	}
	return result.completion, nil
}

func (c *Client) ChatBlock(ctx context.Context, request ChatRequest) (Completion, error) {
	messages, options, err := toNativeRequest(request)
	if err != nil {
		return Completion{}, err
	}
	result, err := c.runNative(ctx, messages, options, request.ResponseFormat, false, nil, nil)
	if err != nil {
		return result.completion, err
	}
	return result.completion, nil
}

func emitBlockItems(ctx context.Context, completion Completion, handler StreamHandler) error {
	if completion.Reasoning != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		handler(StreamItem{Kind: StreamReasoning, Text: completion.Reasoning})
	}
	if completion.Content != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		handler(StreamItem{Kind: StreamDelta, Text: completion.Content})
	}
	for _, call := range completion.ToolCalls {
		if err := ctx.Err(); err != nil {
			return err
		}
		handler(StreamItem{Kind: StreamToolArgs, Name: call.Function.Name, Chars: len(call.Function.Arguments)})
	}
	return nil
}
