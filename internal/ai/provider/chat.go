package provider

import (
	"context"
)

func (c *Client) ChatBlock(ctx context.Context, request ChatRequest) (Completion, error) {
	messages, options, err := toNativeRequest(request)
	if err != nil {
		return Completion{}, err
	}
	result, err := c.runNative(ctx, messages, options, false, nil)
	if err != nil {
		return result.completion, err
	}
	return result.completion, nil
}
