package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const compactionTriggerPercent = 70

func compactionTriggerTokens(window uint64) int {
	return int(window * compactionTriggerPercent / 100)
}

func estimateHistoryTokens(messages []*schema.Message) int {
	return estimatedMessages(messages) / 3
}

type compactionHandler struct {
	*adk.BaseChatModelAgentMiddleware
	summary   adk.ChatModelAgentMiddleware
	window    uint64
	onCompact func()
}

func newCompactionHandler(ctx context.Context, chatModel model.BaseChatModel, window uint64, onCompact func()) (*compactionHandler, error) {
	handler := &compactionHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, window: window, onCompact: onCompact}
	if chatModel == nil || window == 0 {
		return handler, nil
	}
	summary, err := summarization.New(ctx, &summarization.Config{
		Model: chatModel,
		TokenCounter: func(_ context.Context, input *summarization.TokenCounterInput) (int, error) {
			return estimateHistoryTokens(input.Messages), nil
		},
		Trigger:         &summarization.TriggerCondition{ContextTokens: compactionTriggerTokens(window)},
		UserInstruction: historySummaryInstruction(),
		GenModelInput:   compactionGenModelInput(window),
		Finalize:        compactionFinalize,
	})
	if err != nil {
		return nil, err
	}
	handler.summary = summary
	return handler, nil
}

func (h *compactionHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	after := state
	if h.summary != nil {
		_, next, err := h.summary.BeforeModelRewriteState(ctx, state, nil)
		switch {
		case err == nil:
			after = next
		case ctx.Err() != nil:
			return ctx, nil, err
		}
	}
	messages, compacted, err := fitMessageBudget(after.Messages, h.window)
	if err != nil {
		return ctx, nil, err
	}
	if after == state && !compacted {
		return ctx, state, nil
	}
	if h.onCompact != nil {
		h.onCompact()
	}
	final := *after
	final.Messages = messages
	return ctx, &final, nil
}

func historySummaryInstruction() string {
	return "请把以上对话历史压缩成一份中文摘要，后续轮次将用摘要替代原始历史。必须保留：1) 用户的关键要求、约束与偏好；2) 执行过的命令、工作目录、主机/容器/会话标识；3) 每个工具调用的结论（成功或失败、关键输出、报错与修复过程）；4) 涉及的文件路径与修改要点；5) 尚未完成的步骤与下一步计划。用分点短句书写，不要复述寒暄与重复输出。"
}

func compactionGenModelInput(window uint64) summarization.GenModelInputFunc {
	return func(_ context.Context, sysInstruction, userInstruction *schema.Message, originalMsgs []*schema.Message) ([]*schema.Message, error) {
		rest := originalMsgs
		for len(rest) > 0 && rest[0].Role == schema.System {
			rest = rest[1:]
		}
		fitted, _, err := fitMessageBudget(rest, window)
		if err != nil {
			return nil, err
		}
		input := make([]*schema.Message, 0, len(fitted)+2)
		input = append(input, sysInstruction)
		input = append(input, fitted...)
		input = append(input, userInstruction)
		return input, nil
	}
}

func compactionFinalize(_ context.Context, originalMessages []*schema.Message, rawSummary *schema.Message) ([]*schema.Message, error) {
	content := summaryText(rawSummary)
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("历史摘要内容为空")
	}
	var system []*schema.Message
	rest := originalMessages
	for len(rest) > 0 && rest[0].Role == schema.System {
		system = append(system, rest[0])
		rest = rest[1:]
	}
	retained := retainRecentUnit(rest)
	result := make([]*schema.Message, 0, len(system)+1+len(retained))
	result = append(result, system...)
	result = append(result, schema.UserMessage("[历史摘要]\n"+content))
	result = append(result, retained...)
	return result, nil
}

func summaryText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	var parts []string
	for _, part := range message.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return message.Content
}

func retainRecentUnit(messages []*schema.Message) []*schema.Message {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == schema.User {
			return messages[index:]
		}
	}
	return nil
}
