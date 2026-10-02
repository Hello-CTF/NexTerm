package provider

import (
	"context"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

func (c *Client) ChatModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	return c.newChatModel(ctx, c.config.Model)
}

func (c *Client) newChatModel(ctx context.Context, servingModel string) (model.ToolCallingChatModel, error) {
	temperature := float32(c.config.Temperature)
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: c.config.BaseURL, APIKey: c.config.APIKey, Model: servingModel,
		Temperature: &temperature, HTTPClient: c.http,
	})
}
