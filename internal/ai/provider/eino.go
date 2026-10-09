package provider

import (
	"context"
	"errors"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var errStreamClosed = errors.New("AI stream consumer closed")

func (c *Client) ChatModel(_ context.Context) (model.ToolCallingChatModel, error) {
	return &resilientChatModel{client: c}, nil
}

func (c *Client) BaseChatModel(ctx context.Context) (model.BaseChatModel, uint64, error) {
	chatModel, err := c.ChatModel(ctx)
	if err != nil {
		return nil, 0, err
	}
	return chatModel, c.config.ContextWindow, nil
}

type resilientChatModel struct {
	client *Client
	tools  []*schema.ToolInfo
}

func (m *resilientChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	result, err := m.client.runNative(ctx, input, m.withBoundTools(opts), false, nil)
	if err != nil {
		return nil, err
	}
	return result.message, nil
}

func (m *resilientChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	options := m.withBoundTools(opts)
	go func() {
		defer writer.Close()
		onFrame := func(message *schema.Message) error {
			if writer.Send(message, nil) {
				return errStreamClosed
			}
			return nil
		}
		_, err := m.client.runNative(ctx, input, options, true, onFrame)
		if err != nil && !errors.Is(err, errStreamClosed) {
			writer.Send(nil, err)
		}
	}()
	return reader, nil
}

func (m *resilientChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cloned := make([]*schema.ToolInfo, len(tools))
	copy(cloned, tools)
	return &resilientChatModel{client: m.client, tools: cloned}, nil
}

func (m *resilientChatModel) withBoundTools(opts []model.Option) []model.Option {
	if m.tools == nil || model.GetCommonOptions(nil, opts...).Tools != nil {
		return opts
	}
	options := make([]model.Option, len(opts), len(opts)+1)
	copy(options, opts)
	return append(options, model.WithTools(m.tools))
}

func (c *Client) newChatModel(ctx context.Context, servingModel string) (model.ToolCallingChatModel, error) {
	config := &openai.ChatModelConfig{
		BaseURL: c.config.BaseURL, APIKey: c.config.APIKey, Model: servingModel,
		HTTPClient: c.http, MaxTokens: c.config.MaxTokens,
	}
	if c.config.Temperature != nil {
		temperature := float32(*c.config.Temperature)
		config.Temperature = &temperature
	}
	if c.config.ReasoningEffort != "" {
		config.ReasoningEffort = openai.ReasoningEffortLevel(c.config.ReasoningEffort)
	}
	return openai.NewChatModel(ctx, config)
}
