package aicontext

import (
	"context"
	"fmt"
	"strings"
)

const (
	RefMaxCount    = 4
	RefScreenRunes = 4000
	RefFileRunes   = 6000
	RefTotalRunes  = 12000
)

type RefSpec struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	SessionID string `json:"sessionId,omitempty"`
	Path      string `json:"path,omitempty"`
}

type RefFileReader func(ctx context.Context, sessionID, path string) (string, error)

// RenderRefs 把 @ 引用对象展开为注入模型的真实内容：终端标签取当前屏幕（尾截断保留最新输出），
// 会话取连接摘要，文件取内容（头截断）。整体受 RefTotalRunes 预算限制，逐项失败后标注原因继续。
func (b *Builder) RenderRefs(ctx context.Context, refs []RefSpec, readFile RefFileReader) string {
	if len(refs) == 0 {
		return ""
	}
	if len(refs) > RefMaxCount {
		refs = refs[:RefMaxCount]
	}
	var output strings.Builder
	remaining := RefTotalRunes
	for _, ref := range refs {
		if remaining <= 0 {
			break
		}
		section := b.renderRef(ctx, ref, readFile, remaining)
		if section == "" {
			continue
		}
		output.WriteString(section)
		remaining -= len([]rune(section))
	}
	return output.String()
}

func (b *Builder) renderRef(ctx context.Context, ref RefSpec, readFile RefFileReader, budget int) string {
	label := strings.ReplaceAll(strings.TrimSpace(ref.Label), "\n", " ")
	if label == "" {
		label = ref.ID
	}
	switch ref.Kind {
	case "tab":
		if b.deps.Screen == nil || ref.ID == "" {
			return ""
		}
		screen, err := b.deps.Screen(ctx, ref.ID)
		if err != nil {
			return fmt.Sprintf("- %s（无法读取终端屏幕）\n", label)
		}
		if strings.TrimSpace(screen.Text) == "" {
			return fmt.Sprintf("- %s（终端屏幕为空）\n", label)
		}
		return fmt.Sprintf("[引用：%s 的终端屏幕]\n%s\n", label, truncateRunes(screen.Text, min(RefScreenRunes, budget), true))
	case "asset":
		if b.deps.Session == nil || ref.ID == "" {
			return ""
		}
		session, err := b.deps.Session(ctx, ref.ID)
		if err != nil {
			return fmt.Sprintf("- %s（无法读取会话信息）\n", label)
		}
		var brief strings.Builder
		fmt.Fprintf(&brief, "- 连接：%s（%s）\n", session.Name, session.Kind)
		if session.Host != "" {
			fmt.Fprintf(&brief, "- 主机：%s\n", session.Host)
		}
		if session.Username != "" {
			fmt.Fprintf(&brief, "- 用户：%s\n", session.Username)
		}
		if session.CWD != "" {
			fmt.Fprintf(&brief, "- 当前目录：%s\n", session.CWD)
		}
		return fmt.Sprintf("[引用：会话 %s]\n%s", label, truncateRunes(brief.String(), budget, false))
	case "file":
		if readFile == nil || ref.Path == "" {
			return ""
		}
		content, err := readFile(ctx, ref.SessionID, ref.Path)
		if err != nil {
			return fmt.Sprintf("- %s（无法读取文件 %s）\n", label, ref.Path)
		}
		return fmt.Sprintf("[引用：文件 %s]\n%s\n", ref.Path, truncateRunes(content, min(RefFileRunes, budget), false))
	default:
		return ""
	}
}

func truncateRunes(value string, limit int, keepTail bool) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if keepTail {
		return "[前文已截断]\n" + string(runes[len(runes)-limit:])
	}
	return string(runes[:limit]) + "\n[内容已截断]"
}
