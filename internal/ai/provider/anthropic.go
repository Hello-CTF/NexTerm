package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const DefaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicContentKey carries, on the final frame of an Anthropic response,
// the content blocks exactly as received and a fingerprint of the request
// they answer. It is the only place thinking signatures live: the next
// request of the same run sends the blocks back verbatim, and the fingerprint
// tells whether what preceded them is still unchanged. The value is a string
// because streamed frames merge string extras by concatenation.
const anthropicContentKey = "anthropic_content"

const missingToolResultText = "No result was recorded for this tool call."

type anthropicCarrier struct {
	Prefix  string            `json:"prefix"`
	Content []json.RawMessage `json:"content"`
}

// anthropicAPIBase returns the base the SDK appends "v1/..." to, accepting a
// base URL typed with or without the /v1 suffix.
func anthropicAPIBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return DefaultAnthropicBaseURL + "/"
	}
	base = strings.TrimSuffix(base, "/v1")
	return base + "/"
}

func (c *Client) anthropicSDK() anthropic.Client {
	options := []option.RequestOption{
		// Credentials and the endpoint come from the profile only, never from
		// the host's ANTHROPIC_* environment or CLI profile files.
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(anthropicAPIBase(c.config.BaseURL)),
		option.WithHTTPClient(c.http),
		// The client's own retry policy, circuit breaker and fallback model
		// already wrap every attempt.
		option.WithMaxRetries(0),
	}
	if c.config.APIKey != "" {
		options = append(options, option.WithAPIKey(c.config.APIKey))
	}
	return anthropic.NewClient(options...)
}

type anthropicChatModel struct {
	client *Client
	model  string
	sdk    anthropic.Client
}

func (c *Client) newAnthropicChatModel(servingModel string) *anthropicChatModel {
	return &anthropicChatModel{client: c, model: servingModel, sdk: c.anthropicSDK()}
}

func (m *anthropicChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	var frames []*schema.Message
	if err := m.run(ctx, input, opts, func(frame *schema.Message) bool {
		frames = append(frames, frame)
		return true
	}); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, errors.New("AI block response has no message")
	}
	return schema.ConcatMessages(frames)
}

func (m *anthropicChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer func() {
			cancel()
			writer.Close()
		}()
		err := m.run(ctx, input, opts, func(frame *schema.Message) bool {
			if writer.Send(frame, nil) {
				cancel()
				return false
			}
			return true
		})
		if err != nil {
			writer.Send(nil, err)
		}
	}()
	return reader, nil
}

func (m *anthropicChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

var errAnthropicConsumerClosed = errors.New("AI stream consumer closed")

func (m *anthropicChatModel) run(ctx context.Context, input []*schema.Message, opts []model.Option, emit func(*schema.Message) bool) error {
	options := model.GetCommonOptions(nil, opts...)
	servingModel := m.model
	if options.Model != nil && strings.TrimSpace(*options.Model) != "" {
		servingModel = strings.TrimSpace(*options.Model)
	}
	request, err := buildAnthropicRequest(m.client.config, servingModel, input, options, false)
	if err != nil {
		return err
	}
	err = m.stream(ctx, request, emit)
	if err != nil && request.replaysThinking && isThinkingPrefixMismatch(err) {
		// The thinking sent back no longer matches what preceded it, which
		// the fingerprint check failed to catch. Leaving out every earlier
		// thinking block is always accepted, so retry once without them.
		request, err = buildAnthropicRequest(m.client.config, servingModel, input, options, true)
		if err != nil {
			return err
		}
		err = m.stream(ctx, request, emit)
	}
	if errors.Is(err, errAnthropicConsumerClosed) {
		return nil
	}
	return err
}

func isThinkingPrefixMismatch(err error) bool {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		return false
	}
	return strings.Contains(apiErr.Message, "bound to a different conversation") ||
		strings.Contains(apiErr.RawJSON(), "bound to a different conversation")
}

type anthropicBlockState struct {
	kind     string
	ordinal  int
	argsSent bool
	emitted  bool
}

func (m *anthropicChatModel) stream(ctx context.Context, request anthropicRequest, emit func(*schema.Message) bool) error {
	state := attemptFromContext(ctx)
	stream := m.sdk.Messages.NewStreaming(ctx, request.params)
	defer stream.Close()
	var message anthropic.Message
	blocks := map[int64]*anthropicBlockState{}
	tools := 0
	reasoningEmitted := false
	send := func(frame *schema.Message) error {
		frame.Role = schema.Assistant
		if !emit(frame) {
			return errAnthropicConsumerClosed
		}
		return nil
	}
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return fmt.Errorf("decode AI event stream: %w", err)
		}
		switch event.Type {
		case "message_start":
			if state != nil {
				state.setServingModel(strings.TrimSpace(event.Message.Model))
				state.setCacheCreation(uint64(max(event.Message.Usage.CacheCreationInputTokens, 0)))
			}
		case "content_block_start":
			block := &anthropicBlockState{kind: event.ContentBlock.Type}
			blocks[event.Index] = block
			switch block.kind {
			case "tool_use":
				block.ordinal = tools
				tools++
				index := block.ordinal
				if err := send(&schema.Message{ToolCalls: []schema.ToolCall{{
					Index: &index, ID: event.ContentBlock.ID, Type: "function",
					Function: schema.FunctionCall{Name: event.ContentBlock.Name},
				}}}); err != nil {
					return err
				}
			}
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					if err := send(&schema.Message{Content: event.Delta.Text}); err != nil {
						return err
					}
				}
			case "thinking_delta":
				if event.Delta.Thinking == "" {
					continue
				}
				text := event.Delta.Thinking
				if reasoningEmitted && !block.emitted {
					// Separate the summaries of successive thinking blocks.
					text = "\n\n" + text
				}
				block.emitted, reasoningEmitted = true, true
				if err := send(&schema.Message{ReasoningContent: text}); err != nil {
					return err
				}
			case "input_json_delta":
				if block.kind != "tool_use" || event.Delta.PartialJSON == "" {
					continue
				}
				block.argsSent = true
				index := block.ordinal
				if err := send(&schema.Message{ToolCalls: []schema.ToolCall{{
					Index: &index, Function: schema.FunctionCall{Arguments: event.Delta.PartialJSON},
				}}}); err != nil {
					return err
				}
			}
		case "content_block_stop":
			block := blocks[event.Index]
			if block != nil && block.kind == "tool_use" && !block.argsSent {
				block.argsSent = true
				index := block.ordinal
				if err := send(&schema.Message{ToolCalls: []schema.ToolCall{{
					Index: &index, Function: schema.FunctionCall{Arguments: "{}"},
				}}}); err != nil {
					return err
				}
			}
		case "message_delta":
			if state != nil && event.Usage.JSON.CacheCreationInputTokens.Valid() {
				state.setCacheCreation(uint64(max(event.Usage.CacheCreationInputTokens, 0)))
			}
		}
	}
	if err := stream.Err(); err != nil {
		return anthropicStreamError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if message.Type == "" && message.ID == "" && len(message.Content) == 0 {
		return io.ErrUnexpectedEOF
	}
	if message.StopReason == anthropic.StopReasonRefusal {
		return &RefusalError{Category: string(message.StopDetails.Category), Explanation: message.StopDetails.Explanation}
	}
	final := &schema.Message{ResponseMeta: &schema.ResponseMeta{
		FinishReason: anthropicFinishReason(message.StopReason),
		Usage:        anthropicTokenUsage(message.Usage),
	}}
	carrier := anthropicCarrier{Prefix: request.fingerprint, Content: make([]json.RawMessage, 0, len(message.Content))}
	for _, block := range message.Content {
		if raw := block.RawJSON(); raw != "" {
			carrier.Content = append(carrier.Content, json.RawMessage(raw))
		}
	}
	if encoded, err := json.Marshal(carrier); err == nil {
		final.Extra = map[string]any{anthropicContentKey: string(encoded)}
	}
	return send(final)
}

// RefusalError reports a response Claude's safety classifiers declined.
type RefusalError struct {
	Category    string
	Explanation string
}

func (e *RefusalError) Error() string {
	detail := strings.TrimSpace(e.Explanation)
	if e.Category != "" {
		detail = strings.TrimSpace(e.Category + " " + detail)
	}
	if detail == "" {
		return "模型拒绝了这次请求"
	}
	return "模型拒绝了这次请求：" + truncate(detail, 300)
}

func anthropicFinishReason(reason anthropic.StopReason) string {
	switch reason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
		return "stop"
	case anthropic.StopReasonMaxTokens, anthropic.StopReasonModelContextWindowExceeded:
		return "length"
	case anthropic.StopReasonToolUse:
		return "tool_calls"
	default:
		return string(reason)
	}
}

// anthropicTokenUsage counts cached input as part of the prompt, as the
// OpenAI-compatible usage the rest of the client reports does.
func anthropicTokenUsage(value anthropic.Usage) *schema.TokenUsage {
	input := max(value.InputTokens, 0)
	read := max(value.CacheReadInputTokens, 0)
	write := max(value.CacheCreationInputTokens, 0)
	output := max(value.OutputTokens, 0)
	prompt := int(input + read + write)
	usage := &schema.TokenUsage{PromptTokens: prompt, CompletionTokens: int(output), TotalTokens: prompt + int(output)}
	usage.PromptTokenDetails.CachedTokens = int(read)
	usage.PromptTokenDetails.CacheWriteTokens = int(write)
	return usage
}

// anthropicStreamError gives an error event that arrives inside a 200 stream
// the HTTP status its type stands for, so it is classified and retried like
// the same error returned before the stream started.
func anthropicStreamError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 200 && apiErr.StatusCode < 300 {
		return &streamEventError{status: anthropicErrorStatus(string(apiErr.Type())), message: apiErr.Message}
	}
	const prefix = "received error while streaming: "
	if text := err.Error(); strings.HasPrefix(text, prefix) {
		var payload struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(text, prefix)), &payload) == nil && payload.Error.Type != "" {
			return &streamEventError{status: anthropicErrorStatus(payload.Error.Type), message: payload.Error.Message}
		}
	}
	return err
}

type streamEventError struct {
	status  int
	message string
}

func (e *streamEventError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("AI stream error: HTTP %d", e.status)
	}
	return fmt.Sprintf("AI stream error: HTTP %d: %s", e.status, truncate(e.message, 300))
}

func anthropicErrorStatus(kind string) int {
	switch kind {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error", "billing_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "timeout_error":
		return http.StatusGatewayTimeout
	case "overloaded_error":
		return 529
	default:
		return http.StatusInternalServerError
	}
}

type anthropicRequest struct {
	params          anthropic.MessageNewParams
	fingerprint     string
	replaysThinking bool
}

func anthropicMaxTokens(config Config) int64 {
	if config.MaxTokens != nil && *config.MaxTokens > 0 {
		return int64(*config.MaxTokens)
	}
	return int64(max(min(uint64(MaxTokensHardLimit), config.ContextWindow/2), 1))
}

func anthropicEffort(effort ReasoningEffort) anthropic.OutputConfigEffort {
	switch strings.ToLower(string(effort)) {
	case "minimal", "low":
		return anthropic.OutputConfigEffortLow
	case "medium":
		return anthropic.OutputConfigEffortMedium
	case "high":
		return anthropic.OutputConfigEffortHigh
	case "xhigh":
		return anthropic.OutputConfigEffortXhigh
	case "max":
		return anthropic.OutputConfigEffortMax
	default:
		return ""
	}
}

func anthropicThinking(config Config, maxTokens int64) anthropic.ThinkingConfigParamUnion {
	switch config.Thinking {
	case ThinkingOff:
		return anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
	case ThinkingBudget:
		budget := int64(DefaultThinkingBudget)
		if config.ThinkingBudget != nil {
			budget = int64(*config.ThinkingBudget)
		}
		budget = min(budget, maxTokens-1)
		if budget < MinThinkingBudget {
			// A budget below the API minimum leaves no room to think within max_tokens.
			return anthropic.ThinkingConfigParamUnion{}
		}
		return anthropic.ThinkingConfigParamUnion{OfEnabled: &anthropic.ThinkingConfigEnabledParam{BudgetTokens: budget}}
	default:
		// The reasoning stream shows the summary; the default display on
		// current models is an empty string.
		return anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized}}
	}
}

// buildAnthropicRequest converts the eino conversation. Leading system
// messages become the system prompt; the first one is the stable instruction
// and carries the cache breakpoint, so tools and that instruction are cached
// whatever volatile system text (memory, the privacy notice) follows it. A
// top-level breakpoint caches the conversation as it grows.
func buildAnthropicRequest(config Config, servingModel string, input []*schema.Message, options *model.Options, stripThinking bool) (anthropicRequest, error) {
	maxTokens := anthropicMaxTokens(config)
	if options != nil && options.MaxTokens != nil && *options.MaxTokens > 0 {
		maxTokens = int64(*options.MaxTokens)
	}
	params := anthropic.MessageNewParams{Model: anthropic.Model(servingModel), MaxTokens: maxTokens}
	params.Thinking = anthropicThinking(config, maxTokens)
	if effort := anthropicEffort(config.ReasoningEffort); effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: effort}
	}
	var tools []*schema.ToolInfo
	if options != nil {
		tools = options.Tools
		if options.ToolChoice != nil && *options.ToolChoice == schema.ToolChoiceForbidden && len(tools) != 0 {
			params.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
		}
		if len(options.Stop) != 0 {
			params.StopSequences = options.Stop
		}
	}
	for _, tool := range tools {
		converted, err := anthropicTool(tool)
		if err != nil {
			return anthropicRequest{}, err
		}
		params.Tools = append(params.Tools, converted)
	}

	position := 0
	for position < len(input) && (input[position] == nil || input[position].Role == schema.System) {
		if message := input[position]; message != nil {
			params.System = append(params.System, anthropicSystemBlocks(message.Content)...)
		}
		position++
	}
	for index := range params.System {
		if params.System[index].Text != privacyInstruction {
			params.System[index].CacheControl = anthropic.NewCacheControlEphemeralParam()
			break
		}
	}

	builder := anthropicMessageBuilder{stripThinking: stripThinking, tools: params.Tools, system: params.System}
	for _, message := range input[position:] {
		if message == nil {
			continue
		}
		if err := builder.add(message); err != nil {
			return anthropicRequest{}, err
		}
	}
	builder.closeToolTurn()
	params.Messages = builder.messages
	if len(params.Messages) == 0 {
		return anthropicRequest{}, errors.New("AI request has no messages")
	}
	if params.Messages[0].OfUser == nil {
		params.Messages = append([]anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("(earlier conversation omitted)"))}, params.Messages...)
	}
	params.CacheControl = anthropic.NewCacheControlEphemeralParam()
	return anthropicRequest{params: params, fingerprint: builder.fingerprint(len(params.Messages)), replaysThinking: builder.replayed}, nil
}

// anthropicSystemBlocks splits off the privacy notice the redaction layer
// prepends, so the instruction after it stays byte-identical and cacheable.
func anthropicSystemBlocks(content string) []anthropic.TextBlockParam {
	var blocks []anthropic.TextBlockParam
	if rest, ok := strings.CutPrefix(content, privacyInstruction); ok {
		blocks = append(blocks, anthropic.TextBlockParam{Text: privacyInstruction})
		content = strings.TrimPrefix(rest, "\n\n")
	}
	if strings.TrimSpace(content) != "" {
		blocks = append(blocks, anthropic.TextBlockParam{Text: content})
	}
	// The notice goes last so it never shifts the cached instruction.
	if len(blocks) == 2 {
		blocks[0], blocks[1] = blocks[1], blocks[0]
	}
	return blocks
}

func anthropicTool(tool *schema.ToolInfo) (anthropic.ToolUnionParam, error) {
	if tool == nil {
		return anthropic.ToolUnionParam{}, errors.New("AI tool definition is nil")
	}
	converted := anthropic.ToolParam{Name: tool.Name}
	if tool.Desc != "" {
		converted.Description = anthropic.String(tool.Desc)
	}
	var schemaObject map[string]any
	if tool.ParamsOneOf != nil {
		parameters, err := tool.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return anthropic.ToolUnionParam{}, fmt.Errorf("encode AI tool parameters: %w", err)
		}
		if parameters != nil {
			encoded, err := json.Marshal(parameters)
			if err != nil {
				return anthropic.ToolUnionParam{}, fmt.Errorf("encode AI tool parameters: %w", err)
			}
			if err := json.Unmarshal(encoded, &schemaObject); err != nil {
				return anthropic.ToolUnionParam{}, fmt.Errorf("decode AI tool parameters: %w", err)
			}
		}
	}
	properties, ok := schemaObject["properties"]
	if !ok || properties == nil {
		properties = map[string]any{}
	}
	converted.InputSchema.Properties = properties
	if required, ok := schemaObject["required"].([]any); ok {
		for _, name := range required {
			if text, ok := name.(string); ok {
				converted.InputSchema.Required = append(converted.InputSchema.Required, text)
			}
		}
	}
	extra := map[string]any{}
	for key, value := range schemaObject {
		switch key {
		case "type", "properties", "required":
		default:
			extra[key] = value
		}
	}
	if len(extra) != 0 {
		converted.InputSchema.ExtraFields = extra
	}
	return anthropic.ToolUnionParam{OfTool: &converted}, nil
}

type anthropicMessageBuilder struct {
	messages      []anthropic.MessageParam
	tools         []anthropic.ToolUnionParam
	system        []anthropic.TextBlockParam
	stripThinking bool
	// pending holds the tool_use IDs of the last assistant turn that no
	// tool_result answered yet.
	pending []string
	// carried is set once a reply with its content carrier was replayed;
	// broken once the run of replayable thinking was interrupted, after
	// which no later thinking may be sent.
	carried  bool
	broken   bool
	replayed bool
}

func (b *anthropicMessageBuilder) last(role string) []anthropic.ContentBlockParamUnion {
	if len(b.messages) == 0 {
		return nil
	}
	message := b.messages[len(b.messages)-1]
	switch {
	case role == "user" && message.OfUser != nil:
		return message.OfUser.Content
	case role == "assistant" && message.OfAssistant != nil:
		return message.OfAssistant.Content
	}
	return nil
}

func (b *anthropicMessageBuilder) append(role string, blocks ...anthropic.ContentBlockParamUnion) {
	if len(blocks) == 0 {
		return
	}
	if len(b.messages) != 0 {
		message := &b.messages[len(b.messages)-1]
		if role == "user" && message.OfUser != nil {
			message.OfUser.Content = append(message.OfUser.Content, blocks...)
			return
		}
		if role == "assistant" && message.OfAssistant != nil {
			message.OfAssistant.Content = append(message.OfAssistant.Content, blocks...)
			return
		}
	}
	if role == "user" {
		b.messages = append(b.messages, anthropic.NewUserMessage(blocks...))
	} else {
		b.messages = append(b.messages, anthropic.NewAssistantMessage(blocks...))
	}
}

// closeToolTurn answers every tool call that has no result yet, since the API
// rejects a tool_use that the next message does not answer.
func (b *anthropicMessageBuilder) closeToolTurn() {
	for _, id := range b.pending {
		b.append("user", anthropic.NewToolResultBlock(id, missingToolResultText, true))
	}
	b.pending = nil
}

func (b *anthropicMessageBuilder) add(message *schema.Message) error {
	switch message.Role {
	case schema.Tool:
		return b.addToolResult(message)
	case schema.Assistant:
		b.closeToolTurn()
		return b.addAssistant(message)
	case schema.System:
		b.closeToolTurn()
		if strings.TrimSpace(message.Content) != "" {
			b.append("user", anthropic.NewTextBlock("[System]\n"+message.Content))
		}
		return nil
	default:
		b.closeToolTurn()
		blocks, err := anthropicUserBlocks(message)
		if err != nil {
			return err
		}
		b.append("user", blocks...)
		return nil
	}
}

func (b *anthropicMessageBuilder) addToolResult(message *schema.Message) error {
	position := -1
	for index, id := range b.pending {
		if id == message.ToolCallID {
			position = index
			break
		}
	}
	content := message.Content
	if position < 0 {
		// A result whose call is not the turn just before it cannot be a
		// tool_result block; keep its text as plain context instead.
		b.closeToolTurn()
		if strings.TrimSpace(content) != "" {
			b.append("user", anthropic.NewTextBlock("[Tool result "+message.ToolCallID+"]\n"+content))
		}
		return nil
	}
	b.pending = append(b.pending[:position], b.pending[position+1:]...)
	if strings.TrimSpace(content) == "" {
		content = "(empty)"
	}
	b.append("user", anthropic.NewToolResultBlock(message.ToolCallID, content, toolResultFailed(content)))
	return nil
}

// toolResultFailed reads the ok flag of the tool output envelope.
func toolResultFailed(content string) bool {
	var envelope struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal([]byte(content), &envelope) != nil || envelope.OK == nil {
		return false
	}
	return !*envelope.OK
}

func (b *anthropicMessageBuilder) addAssistant(message *schema.Message) error {
	startsTurn := b.last("assistant") == nil
	if blocks, ok := b.carriedBlocks(message, startsTurn); ok {
		b.append("assistant", blocks...)
		b.pendingFrom(blocks)
		return nil
	}
	if b.carried {
		// A reply without a usable carrier interrupts the run of thinking.
		b.broken = true
	}
	var blocks []anthropic.ContentBlockParamUnion
	text := message.Content
	if text == "" {
		text = nativeTextParts(message)
	}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, anthropic.NewTextBlock(text))
	}
	for _, call := range message.ToolCalls {
		if call.ID == "" {
			continue
		}
		blocks = append(blocks, anthropic.ContentBlockParamUnion{OfToolUse: &anthropic.ToolUseBlockParam{
			ID: call.ID, Name: call.Function.Name, Input: toolInput(call.Function.Arguments),
		}})
	}
	b.append("assistant", blocks...)
	b.pendingFrom(blocks)
	return nil
}

func toolInput(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || !json.Valid([]byte(trimmed)) || !strings.HasPrefix(trimmed, "{") {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

func (b *anthropicMessageBuilder) pendingFrom(blocks []anthropic.ContentBlockParamUnion) {
	for _, block := range blocks {
		if block.OfToolUse != nil {
			b.pending = append(b.pending, block.OfToolUse.ID)
		}
	}
}

// carriedBlocks returns a reply's blocks exactly as the API produced them,
// thinking included, when that is still valid to send back: the reply opens
// its own turn, its tool calls are the ones the conversation records, nothing
// before it changed since it was produced, and no reply between it and the
// previous replayed one dropped its thinking.
func (b *anthropicMessageBuilder) carriedBlocks(message *schema.Message, startsTurn bool) ([]anthropic.ContentBlockParamUnion, bool) {
	raw, _ := message.Extra[anthropicContentKey].(string)
	if raw == "" || b.stripThinking || b.broken || !startsTurn {
		return nil, false
	}
	var carrier anthropicCarrier
	if json.Unmarshal([]byte(raw), &carrier) != nil || carrier.Prefix == "" {
		return nil, false
	}
	if carrier.Prefix != b.fingerprint(len(b.messages)) {
		return nil, false
	}
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(carrier.Content))
	var ids []string
	thinking := false
	for _, encoded := range carrier.Content {
		var block anthropic.ContentBlockUnion
		if err := block.UnmarshalJSON(encoded); err != nil {
			return nil, false
		}
		switch block.Type {
		case "thinking", "redacted_thinking":
			thinking = true
		case "tool_use":
			ids = append(ids, block.ID)
		case "text":
		default:
			return nil, false
		}
		blocks = append(blocks, block.ToParam())
	}
	expected := make([]string, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		if call.ID != "" {
			expected = append(expected, call.ID)
		}
	}
	if strings.Join(ids, "\x00") != strings.Join(expected, "\x00") {
		return nil, false
	}
	b.carried = true
	if thinking {
		b.replayed = true
	}
	return blocks, true
}

// fingerprint hashes what a thinking block's validity depends on: the tools,
// the system prompt and the first count messages with thinking left out.
// Cache breakpoints are excluded because they are not content.
func (b *anthropicMessageBuilder) fingerprint(count int) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	_ = encoder.Encode(b.tools)
	for _, block := range b.system {
		_ = encoder.Encode(block.Text)
	}
	for _, message := range b.messages[:min(count, len(b.messages))] {
		role, content := "user", []anthropic.ContentBlockParamUnion(nil)
		if message.OfUser != nil {
			content = message.OfUser.Content
		} else if message.OfAssistant != nil {
			role, content = "assistant", message.OfAssistant.Content
		}
		_ = encoder.Encode(role)
		for _, block := range content {
			if block.OfThinking != nil || block.OfRedactedThinking != nil {
				continue
			}
			_ = encoder.Encode(block)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func anthropicUserBlocks(message *schema.Message) ([]anthropic.ContentBlockParamUnion, error) {
	var blocks []anthropic.ContentBlockParamUnion
	if strings.TrimSpace(message.Content) != "" {
		blocks = append(blocks, anthropic.NewTextBlock(message.Content))
	}
	for _, part := range message.UserInputMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, anthropic.NewTextBlock(part.Text))
			}
		case schema.ChatMessagePartTypeImageURL:
			if part.Image == nil {
				continue
			}
			block, err := anthropicImage(part.Image.Base64Data, part.Image.MIMEType, part.Image.URL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		default:
			return nil, fmt.Errorf("unsupported AI message content part %q", part.Type)
		}
	}
	for _, part := range message.MultiContent {
		switch part.Type {
		case "", schema.ChatMessagePartTypeText:
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, anthropic.NewTextBlock(part.Text))
			}
		case schema.ChatMessagePartTypeImageURL:
			if part.ImageURL == nil {
				continue
			}
			address := part.ImageURL.URL
			block, err := anthropicImage(nil, "", &address)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		default:
			return nil, fmt.Errorf("unsupported AI message content part %q", part.Type)
		}
	}
	return blocks, nil
}

func anthropicImage(data *string, mimeType string, address *string) (anthropic.ContentBlockParamUnion, error) {
	if data == nil && address != nil {
		if metadata, payload, ok := strings.Cut(strings.TrimPrefix(*address, "data:"), ","); strings.HasPrefix(*address, "data:") && ok && strings.HasSuffix(metadata, ";base64") {
			data = &payload
			mimeType = strings.TrimSuffix(metadata, ";base64")
		}
	}
	if data != nil {
		mediaType := anthropic.Base64ImageSourceMediaType(strings.ToLower(strings.TrimSpace(mimeType)))
		switch mediaType {
		case anthropic.Base64ImageSourceMediaTypeImageJPEG, anthropic.Base64ImageSourceMediaTypeImagePNG,
			anthropic.Base64ImageSourceMediaTypeImageGIF, anthropic.Base64ImageSourceMediaTypeImageWebP:
		case "image/jpg":
			mediaType = anthropic.Base64ImageSourceMediaTypeImageJPEG
		default:
			return anthropic.ContentBlockParamUnion{}, fmt.Errorf("unsupported AI image type %q", mimeType)
		}
		return anthropic.ContentBlockParamUnion{OfImage: &anthropic.ImageBlockParam{Source: anthropic.ImageBlockParamSourceUnion{
			OfBase64: &anthropic.Base64ImageSourceParam{Data: *data, MediaType: mediaType},
		}}}, nil
	}
	if address == nil || strings.TrimSpace(*address) == "" {
		return anthropic.ContentBlockParamUnion{}, errors.New("AI image has no data")
	}
	parsed, err := url.Parse(*address)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return anthropic.ContentBlockParamUnion{}, errors.New("unsupported AI image address")
	}
	return anthropic.ContentBlockParamUnion{OfImage: &anthropic.ImageBlockParam{Source: anthropic.ImageBlockParamSourceUnion{
		OfURL: &anthropic.URLImageSourceParam{URL: *address},
	}}}, nil
}

// ListAnthropicModels pages through the Models API.
func (c *Client) listAnthropicModels(ctx context.Context) (ModelList, error) {
	sdk := c.anthropicSDK()
	pager := sdk.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1000)})
	models := make([]string, 0)
	malformed := 0
	for pager.Next() {
		id := strings.TrimSpace(pager.Current().ID)
		if id == "" {
			malformed++
			continue
		}
		models = append(models, id)
	}
	if err := pager.Err(); err != nil {
		return ModelList{}, fmt.Errorf("AI model list request: %w", err)
	}
	return ModelList{Models: models, Malformed: malformed}, nil
}
