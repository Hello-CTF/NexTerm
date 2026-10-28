package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	memorySaveTool   = "memory_save"
	memoryListTool   = "memory_list"
	memoryRecallTool = "memory_recall"
	memoryForgetTool = "memory_forget"
)

const memoryRecallMaxBytes = 16 << 10

const (
	memoryListMaxEntries = 64
	memoryListMaxBytes   = 8 << 10
)

type memorySaveArgs struct {
	Topic   string `json:"topic" jsonschema:"required"`
	Content string `json:"content" jsonschema:"required"`
}

type memoryListArgs struct {
	Topic string `json:"topic,omitempty"`
}

type memoryRecallArgs struct {
	IDs []string `json:"ids" jsonschema:"required"`
}

type memoryForgetArgs struct {
	ID              string `json:"id" jsonschema:"required"`
	ExpectedVersion uint64 `json:"expectedVersion" jsonschema:"required"`
}

func (r *Runner) memoryScope(ctx context.Context) (memory.Scope, error) {
	if r.config.MemoryScopeFor != nil {
		return r.config.MemoryScopeFor(ctx)
	}
	return r.config.MemoryScope, nil
}

func (r *Runner) memoryTools(ctx context.Context, planMode bool) ([]tool.BaseTool, error) {
	if r.config.Memory == nil || planMode {
		return nil, nil
	}
	scope, err := r.memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := r.config.Memory.Settings(ctx, scope)
	if err != nil {
		return nil, err
	}
	if !settings.ToolsEnabled {
		return nil, nil
	}
	store := r.config.Memory
	makers := []func() (tool.InvokableTool, error){
		func() (tool.InvokableTool, error) {
			return utils.InferTool(memorySaveTool, "Write one long-term operational memory into the semantic memory store (filed by topic; a secret check runs before writing).", func(ctx context.Context, input memorySaveArgs) (tools.Output, error) {
				return tools.Guarded(ctx, memorySaveTool, func() (tools.Output, error) {
					return memorySaveOutput(ctx, store, scope, input), nil
				})
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool(memoryListTool, "List the catalog of long-term semantic memories (topic, ID, version), filterable by topic; entry count and bytes are capped, and anything beyond the caps is omitted and marked.", func(ctx context.Context, input memoryListArgs) (tools.Output, error) {
				return tools.Guarded(ctx, memoryListTool, func() (tools.Output, error) {
					return memoryListOutput(ctx, store, scope, input), nil
				})
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool(memoryRecallTool, "Read the full content of a long-term semantic memory by ID.", func(ctx context.Context, input memoryRecallArgs) (tools.Output, error) {
				return tools.Guarded(ctx, memoryRecallTool, func() (tools.Output, error) {
					return memoryRecallOutput(ctx, store, scope, input), nil
				})
			})
		},
		func() (tool.InvokableTool, error) {
			return utils.InferTool(memoryForgetTool, "Delete one long-term semantic memory by ID and version (a version mismatch is rejected).", func(ctx context.Context, input memoryForgetArgs) (tools.Output, error) {
				return tools.Guarded(ctx, memoryForgetTool, func() (tools.Output, error) {
					return memoryForgetOutput(ctx, store, scope, input), nil
				})
			})
		},
	}
	result := make([]tool.BaseTool, 0, len(makers))
	for _, makeTool := range makers {
		current, err := makeTool()
		if err != nil {
			return nil, err
		}
		result = append(result, current)
	}
	return result, nil
}

func memorySaveOutput(ctx context.Context, store *memory.Store, scope memory.Scope, input memorySaveArgs) tools.Output {
	entry, err := store.Create(ctx, scope, memory.CreateInput{Topic: input.Topic, Content: input.Content, Secrets: memory.SecretReject})
	if err != nil {
		if errors.Is(err, memory.ErrSensitiveContent) {
			return tools.Fail(errors.New("内容疑似包含密钥（如 password、token、api_key 的赋值），已拒绝保存；请移除密钥后重试"))
		}
		return tools.Fail(err)
	}
	text := fmt.Sprintf("已保存记忆 %s（主题 %s，版本 %d）", entry.ID, entry.Topic, entry.Version)
	return tools.OK(text)
}

func memoryListOutput(ctx context.Context, store *memory.Store, scope memory.Scope, input memoryListArgs) tools.Output {
	index, err := store.Index(ctx, scope)
	if err != nil {
		return tools.Fail(err)
	}

	var builder strings.Builder
	bytes := 0
	count := 0
	omitted := 0
	for _, topic := range index {
		if input.Topic != "" && topic.Topic != input.Topic {
			continue
		}
		for _, entry := range topic.Entries {
			marker := ""
			if entry.Redacted {
				marker = " [已脱敏]"
			}
			line := fmt.Sprintf("%s\t%s\tv%d%s\n", topic.Topic, entry.ID, entry.Version, marker)
			if count >= memoryListMaxEntries || bytes+len(line) > memoryListMaxBytes {
				omitted++
				continue
			}
			builder.WriteString(line)
			bytes += len(line)
			count++
		}
	}
	if count == 0 && omitted == 0 {
		if input.Topic != "" {
			return tools.OK("（该主题下没有记忆）")
		}
		return tools.OK("（记忆库为空）")
	}
	text := builder.String()
	if omitted > 0 {
		text += fmt.Sprintf("[已省略 %d 条]\n", omitted)
	}
	return tools.OK(text)
}

func memoryRecallOutput(ctx context.Context, store *memory.Store, scope memory.Scope, input memoryRecallArgs) tools.Output {
	if len(input.IDs) == 0 {
		return tools.Fail(errors.New("ids 不能为空"))
	}
	var builder, modelBuilder strings.Builder
	redactions := 0
	for _, id := range input.IDs {
		entry, err := store.Get(ctx, scope, id)
		if err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				return tools.Fail(fmt.Errorf("记忆 %s 不存在", id))
			}
			return tools.Fail(err)
		}

		content, redacted := memory.RedactText(entry.Content)
		marker := ""
		modelMarker := ""
		if entry.Redacted || redacted {
			marker = " [已脱敏]"
			modelMarker = " [redacted]"
		}
		if redacted {
			redactions++
		}
		fmt.Fprintf(&builder, "— %s（主题 %s，版本 %d%s）\n%s\n", entry.ID, entry.Topic, entry.Version, marker, content)
		fmt.Fprintf(&modelBuilder, "- %s (topic %s, version %d%s)\n%s\n", entry.ID, entry.Topic, entry.Version, modelMarker, content)
	}
	text, cut := prefixBytes(builder.String(), memoryRecallMaxBytes)
	modelText, modelCut := prefixBytes(modelBuilder.String(), memoryRecallMaxBytes)
	if redactions > 0 {
		text += fmt.Sprintf("\n[%d 条内容包含疑似密钥，已脱敏]", redactions)
		modelText += fmt.Sprintf("\n[%d entries contained suspected secrets and were redacted]", redactions)
	}
	if cut {
		text += "\n[输出已截断]"
	}
	if cut || modelCut {
		modelText += "\n[output truncated]"
	}
	return tools.Output{OK: true, Text: text, ModelText: modelText, Truncated: cut}
}

func memoryForgetOutput(ctx context.Context, store *memory.Store, scope memory.Scope, input memoryForgetArgs) tools.Output {
	if err := store.Delete(ctx, scope, input.ID, input.ExpectedVersion); err != nil {
		var conflict *memory.VersionConflictError
		if errors.As(err, &conflict) {
			return tools.Fail(fmt.Errorf("记忆 %s 版本冲突（期望 %d，实际 %d），请刷新后重试", conflict.ID, conflict.Expected, conflict.Actual))
		}
		if errors.Is(err, memory.ErrNotFound) {
			return tools.Fail(fmt.Errorf("记忆 %s 不存在", input.ID))
		}
		return tools.Fail(err)
	}
	return tools.OK(fmt.Sprintf("已删除记忆 %s", input.ID))
}
