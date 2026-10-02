package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	MessageExtraRunID         = "run_id"
	MessageExtraCallID        = "call_id"
	MessageExtraServingModel  = "model"
	MessageExtraContextWindow = "context_window"
)

type ExecutionMetadata struct {
	RunID         string
	CallID        string
	Model         string
	ContextWindow uint64
}

func MetadataFromMessage(message *schema.Message) ExecutionMetadata {
	if message == nil {
		return ExecutionMetadata{}
	}
	return ExecutionMetadata{
		RunID:         extraString(message.Extra, MessageExtraRunID),
		CallID:        extraString(message.Extra, MessageExtraCallID),
		Model:         extraString(message.Extra, MessageExtraServingModel),
		ContextWindow: extraUint64(message.Extra, MessageExtraContextWindow),
	}
}

func extraString(extra map[string]any, key string) string {
	value, _ := extra[key].(string)
	return value
}

func extraUint64(extra map[string]any, key string) uint64 {
	switch value := extra[key].(type) {
	case uint64:
		return value
	case int:
		return uint64(max(value, 0))
	case float64:
		return uint64(max(value, 0))
	default:
		return 0
	}
}

type nativeResult struct {
	completion Completion
	message    *schema.Message
}

type nativeFrameHandler func(*schema.Message) error

func (c *Client) runNative(ctx context.Context, input []*schema.Message, options []model.Option, preferStream bool, onFrame nativeFrameHandler, onItem StreamHandler) (nativeResult, error) {
	if err := ctx.Err(); err != nil {
		return nativeResult{}, err
	}
	if onItem == nil {
		onItem = func(StreamItem) {}
	}
	primaryModel := c.config.Model
	if configured := model.GetCommonOptions(nil, options...).Model; configured != nil {
		primaryModel = strings.TrimSpace(*configured)
	}
	models := []string{primaryModel}
	if c.config.FallbackModel != "" && c.config.FallbackModel != primaryModel {
		models = append(models, c.config.FallbackModel)
	}
	runID := ids.New()
	var lastErr error
	for modelIndex, servingModel := range models {
		useBlock := !preferStream
		for retry := 0; retry <= c.retry.MaxRetries; retry++ {
			state := &attemptState{runID: runID, callID: ids.New(), requestedModel: servingModel}
			result, err := c.invokeNative(ctx, input, options, servingModel, useBlock, state, onFrame, onItem)
			if err != nil && !useBlock && ctx.Err() == nil && !state.frameSeen.Load() && !state.emitted && safeStreamFallback(err) {
				useBlock = true
				state = &attemptState{runID: runID, callID: ids.New(), requestedModel: servingModel}
				result, err = c.invokeNative(ctx, input, options, servingModel, true, state, onFrame, onItem)
			}
			if err == nil {
				return result, nil
			}
			lastErr = err
			if ctx.Err() != nil || !replayAllowed(err, state) {
				return nativeResult{}, err
			}
			if retry == c.retry.MaxRetries {
				break
			}
			if err := c.sleep(ctx, c.retry.backoff(retry+1, c.random)); err != nil {
				return nativeResult{}, err
			}
			if err := ctx.Err(); err != nil {
				return nativeResult{}, err
			}
		}
		if modelIndex+1 == len(models) {
			break
		}
		if err := ctx.Err(); err != nil {
			return nativeResult{}, err
		}
	}
	return nativeResult{}, lastErr
}

func (c *Client) invokeNative(ctx context.Context, input []*schema.Message, options []model.Option, servingModel string, block bool, state *attemptState, onFrame nativeFrameHandler, onItem StreamHandler) (nativeResult, error) {
	timeout := c.timeouts.Stream
	if block {
		timeout = c.timeouts.Block
	}
	attemptContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	state.streaming = !block
	attemptContext = state.trackContext(attemptContext)
	chatModel, err := c.newChatModel(attemptContext, servingModel)
	if err != nil {
		return nativeResult{}, err
	}
	attemptOptions := make([]model.Option, len(options), len(options)+1)
	copy(attemptOptions, options)
	attemptOptions = append(attemptOptions, model.WithModel(servingModel))
	trackedItems := func(item StreamItem) {
		state.emitted = true
		onItem(item)
	}
	if block {
		message, err := chatModel.Generate(attemptContext, input, attemptOptions...)
		if err != nil {
			return nativeResult{}, wrapNativeError("AI block chat request", err)
		}
		if message == nil {
			return nativeResult{}, errors.New("AI block response has no message")
		}
		message = c.decorateMessage(message, state)
		completion := c.finishCompletion(completionFromMessage(message), state)
		if onFrame != nil {
			if err := onFrame(message); err != nil {
				return nativeResult{}, err
			}
		}
		if err := emitBlockItems(ctx, completion, trackedItems); err != nil {
			return nativeResult{}, err
		}
		return nativeResult{completion: completion, message: message}, nil
	}

	reader, err := chatModel.Stream(attemptContext, input, attemptOptions...)
	if err != nil {
		return nativeResult{}, wrapNativeError("AI stream chat request", err)
	}
	if reader == nil {
		return nativeResult{}, errors.New("AI stream returned no reader")
	}
	defer reader.Close()
	aggregate := streamState{pending: make(map[uint64]*pendingToolCall), handler: trackedItems, requestedModel: servingModel}
	for {
		message, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nativeResult{}, wrapNativeError("read AI event stream", recvErr)
		}
		state.frameSeen.Store(true)
		if message == nil {
			return nativeResult{}, errors.New("AI stream returned a nil message")
		}
		message = c.decorateMessage(message, state)
		if err := aggregate.consumeMessage(attemptContext, message); err != nil {
			return nativeResult{}, err
		}
		if onFrame != nil {
			if err := onFrame(message); err != nil {
				return nativeResult{}, err
			}
		}
	}
	if err := attemptContext.Err(); err != nil {
		return nativeResult{}, err
	}
	if !aggregate.sawPayload {
		return nativeResult{}, errors.New("AI stream ended without a response payload")
	}
	aggregate.finishTools()
	completion := c.finishCompletion(aggregate.completion, state)
	return nativeResult{completion: completion}, nil
}

type boundedError struct {
	operation string
	err       error
}

func (e *boundedError) Error() string {
	message := e.operation + ": " + e.err.Error()
	if len(message) <= 500 {
		return message
	}
	return truncate(message, 500-len("…"))
}

func (e *boundedError) Unwrap() error {
	return e.err
}

func wrapNativeError(operation string, err error) error {
	if _, ok := statusCodeForError(err); ok {
		return &boundedError{operation: operation, err: err}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func nativeTextParts(message *schema.Message) string {
	var text strings.Builder
	for _, part := range message.MultiContent {
		if part.Type == "" || part.Type == schema.ChatMessagePartTypeText {
			text.WriteString(part.Text)
		}
	}
	if text.Len() != 0 {
		return text.String()
	}
	for _, part := range message.AssistantGenMultiContent {
		if part.Type == "" || part.Type == schema.ChatMessagePartTypeText {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func (c *Client) decorateMessage(message *schema.Message, state *attemptState) *schema.Message {
	cloned := *message
	if cloned.Content == "" {
		cloned.Content = nativeTextParts(message)
	}
	extra := make(map[string]any, len(message.Extra)+4)
	for key, value := range message.Extra {
		extra[key] = value
	}
	state.setServingModel(extraString(extra, MessageExtraServingModel))
	if state.actualModel() == "" {
		state.setServingModel(state.requestedModel)
	}
	extra[MessageExtraRunID] = state.runID
	extra[MessageExtraCallID] = state.callID
	extra[MessageExtraServingModel] = state.actualModel()
	extra[MessageExtraContextWindow] = c.config.ContextWindow
	cloned.Extra = extra
	return &cloned
}

func completionFromMessage(message *schema.Message) Completion {
	completion := Completion{
		Content: message.Content, Reasoning: message.ReasoningContent,
		ToolCalls: normalizeToolCallsFromNative(message.ToolCalls),
	}
	if message.ResponseMeta != nil {
		completion.FinishReason = message.ResponseMeta.FinishReason
		if message.ResponseMeta.Usage != nil {
			completion.Usage = usageFromNative(message.ResponseMeta.Usage)
		}
	}
	return completion
}

func (c *Client) finishCompletion(completion Completion, state *attemptState) Completion {
	completion.RunID = state.runID
	completion.CallID = state.callID
	completion.Model = state.actualModel()
	if completion.Model == "" {
		completion.Model = state.requestedModel
	}
	completion.Usage.RunID = completion.RunID
	completion.Usage.CallID = completion.CallID
	completion.Usage.Model = completion.Model
	completion.Usage.ContextWindow = c.config.ContextWindow
	return completion
}

func usageFromNative(value *schema.TokenUsage) usage.Usage {
	if value == nil {
		return usage.Usage{}
	}
	return usage.Usage{
		PromptTokens: uint64(max(value.PromptTokens, 0)), CompletionTokens: uint64(max(value.CompletionTokens, 0)),
		CachedTokens: uint64(max(value.PromptTokenDetails.CachedTokens, 0)),
	}
}

func normalizeToolCallsFromNative(calls []schema.ToolCall) []ToolCall {
	result := make([]ToolCall, len(calls))
	for index, call := range calls {
		result[index] = ToolCall{
			ID: call.ID, Type: "function",
			Function: FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments},
		}
	}
	return result
}
