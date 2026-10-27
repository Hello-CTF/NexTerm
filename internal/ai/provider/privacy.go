package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// WithPrivacy resolves the current owner's opt-in for each request. A nil
// exchange means disabled; an error must block before any provider is contacted.
func WithPrivacy(resolve func(context.Context) (*privacy.Exchange, error)) Option {
	return func(options *clientOptions) { options.privacy = resolve }
}

func (c *Client) runNative(ctx context.Context, input []*schema.Message, options []model.Option, preferStream bool, onFrame nativeFrameHandler) (nativeResult, error) {
	input = ProjectModelMessages(input)
	if c.privacy == nil {
		return c.runNativePlain(ctx, input, options, preferStream, onFrame)
	}
	exchange, err := c.privacy(ctx)
	if err != nil {
		return nativeResult{}, fmt.Errorf("AI 隐私保护不可用，请检查本地模型：%w", err)
	}
	if exchange == nil {
		return c.runNativePlain(ctx, input, options, preferStream, onFrame)
	}
	input, options, err = protectNativeRequest(ctx, exchange, input, options)
	if err != nil {
		return nativeResult{}, fmt.Errorf("AI 脱敏失败，请求未发送：%w", err)
	}
	var protectedHandler nativeFrameHandler
	var frameErr error
	var frameExtra map[string]any
	text, reasoning := exchange.NewRestorer(), exchange.NewRestorer()
	if onFrame != nil {
		protectedHandler = func(message *schema.Message) error {
			if err := privacyTextResponse(message); err != nil {
				frameErr = err
				return err
			}
			copy := *message
			frameExtra = message.Extra
			copy.Content = text.Push(message.Content, false)
			copy.ReasoningContent = reasoning.Push(message.ReasoningContent, false)
			// Arguments may split inside JSON escapes as well as placeholders.
			// Only emit calls once the complete JSON has been safely restored.
			copy.ToolCalls = nil
			// decorateMessage has already projected text parts into Content.
			copy.MultiContent = nil
			copy.AssistantGenMultiContent = nil
			frameErr = onFrame(&copy)
			return frameErr
		}
	}
	result, requestErr := c.runNativePlain(ctx, input, options, preferStream, protectedHandler)
	result.completion.Content = exchange.Restore(result.completion.Content)
	result.completion.Reasoning = exchange.Restore(result.completion.Reasoning)
	for i := range result.completion.ToolCalls {
		arguments, err := exchange.RestoreJSON(result.completion.ToolCalls[i].Function.Arguments)
		if err != nil {
			result.completion.ToolCalls = nil
			return result, fmt.Errorf("AI 工具参数还原失败：%w", err)
		}
		result.completion.ToolCalls[i].Function.Arguments = arguments
	}
	if result.message != nil {
		if err := privacyTextResponse(result.message); err != nil {
			return nativeResult{}, err
		}
		copy := *result.message
		copy.Content = exchange.Restore(copy.Content)
		copy.MultiContent = nil
		copy.AssistantGenMultiContent = nil
		copy.ReasoningContent = exchange.Restore(copy.ReasoningContent)
		copy.ToolCalls = slices.Clone(copy.ToolCalls)
		for i := range copy.ToolCalls {
			arguments, err := exchange.RestoreJSON(copy.ToolCalls[i].Function.Arguments)
			if err != nil {
				return nativeResult{}, err
			}
			copy.ToolCalls[i].Function.Arguments = arguments
		}
		result.message = &copy
	}
	if onFrame != nil && frameErr == nil {
		last := &schema.Message{Role: schema.Assistant, Content: text.Push("", true), ReasoningContent: reasoning.Push("", true), Extra: frameExtra}
		if requestErr == nil {
			for i, call := range result.completion.ToolCalls {
				last.ToolCalls = append(last.ToolCalls, schema.ToolCall{Index: new(i), ID: call.ID, Type: call.Type, Function: schema.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}})
			}
		}
		if last.Content != "" || last.ReasoningContent != "" || len(last.ToolCalls) > 0 {
			if err := onFrame(last); err != nil {
				return result, err
			}
		}
	}
	return result, requestErr
}

func protectNativeRequest(ctx context.Context, e *privacy.Exchange, input []*schema.Message, options []model.Option) ([]*schema.Message, []model.Option, error) {
	protect := func(text string) (string, error) { return e.Protect(ctx, text) }
	result := make([]*schema.Message, 0, len(input)+1)
	for _, message := range input {
		if message == nil {
			result = append(result, nil)
			continue
		}
		copy := *message
		var err error
		copy.Content, err = protect(message.Content)
		if err != nil {
			return nil, nil, err
		}
		copy.ReasoningContent, err = protect(message.ReasoningContent)
		if err != nil {
			return nil, nil, err
		}
		copy.MultiContent = slices.Clone(message.MultiContent)
		for i := range copy.MultiContent {
			if copy.MultiContent[i].Type != "" && copy.MultiContent[i].Type != schema.ChatMessagePartTypeText {
				return nil, nil, errors.New("本地脱敏仅支持文本，当前请求包含图片或其他媒体")
			}
			copy.MultiContent[i].Text, err = protect(copy.MultiContent[i].Text)
			if err != nil {
				return nil, nil, err
			}
		}
		copy.UserInputMultiContent = slices.Clone(message.UserInputMultiContent)
		for i := range copy.UserInputMultiContent {
			if copy.UserInputMultiContent[i].Type != schema.ChatMessagePartTypeText {
				return nil, nil, errors.New("本地脱敏仅支持文本，当前请求包含图片或其他媒体")
			}
			copy.UserInputMultiContent[i].Text, err = protect(copy.UserInputMultiContent[i].Text)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(copy.AssistantGenMultiContent) > 0 {
			return nil, nil, errors.New("本地脱敏暂不支持多媒体历史消息")
		}
		copy.ToolCalls = slices.Clone(message.ToolCalls)
		for i := range copy.ToolCalls {
			copy.ToolCalls[i].Function.Arguments, err = e.ProtectJSON(ctx, copy.ToolCalls[i].Function.Arguments)
			if err != nil {
				return nil, nil, err
			}
		}
		result = append(result, &copy)
	}
	result = append([]*schema.Message{schema.SystemMessage("Privacy placeholders such as <PRIVATE_EMAIL_...> and <SECRET_...> represent original values retained locally. Preserve each placeholder exactly when referencing that value, including in tool argument string values. Do not infer, modify, or decode them.")}, result...)
	// Match AstrLink's default declaration policy: built-in tool schemas are
	// harness metadata. Their protocol names, enums and documentation stay
	// unchanged; user data in call arguments and results is protected above.
	return result, options, nil
}

func privacyTextResponse(message *schema.Message) error {
	for _, part := range message.MultiContent {
		if part.Type != "" && part.Type != schema.ChatMessagePartTypeText {
			return errors.New("AI 隐私保护暂不支持多媒体回复")
		}
	}
	for _, part := range message.AssistantGenMultiContent {
		if part.Type != "" && part.Type != schema.ChatMessagePartTypeText {
			return errors.New("AI 隐私保护暂不支持多媒体回复")
		}
	}
	return nil
}
