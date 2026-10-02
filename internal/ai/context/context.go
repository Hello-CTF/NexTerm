package aicontext

import (
	"context"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

const BudgetBytes = 64 << 10

type SessionBrief struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Host     string `json:"host,omitempty"`
	Username string `json:"username,omitempty"`
	CWD      string `json:"cwd,omitempty"`
}

type TableBrief struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type Bundle struct {
	Session    *SessionBrief
	Screen     string
	Tail       []string
	Recon      string
	Selection  string
	Tables     []TableBrief
	Containers []tools.Container
}

type Dependencies struct {
	Session    func(context.Context, string) (SessionBrief, error)
	Screen     func(context.Context, string) (tools.Screen, error)
	Tail       func(context.Context, string, int) ([]string, error)
	Tables     func(context.Context, string) ([]TableBrief, error)
	Containers func(context.Context, string) ([]tools.Container, error)
	Transport  tools.TransportResolver
	Now        func() time.Time
	ReconTTL   time.Duration
}

type Builder struct {
	deps  Dependencies
	mu    sync.Mutex
	recon map[string]*reconEntry
}

type reconEntry struct {
	ready   chan struct{}
	expires time.Time
	text    string
}

func NewBuilder(deps Dependencies) *Builder {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.ReconTTL <= 0 {
		deps.ReconTTL = time.Minute
	}
	return &Builder{deps: deps, recon: make(map[string]*reconEntry)}
}

type Prompt struct {
	Stable   string
	Volatile string
	Bundle   Bundle
}

func (b *Builder) Build(ctx context.Context, scope tools.Scope, selection string) Prompt {
	bundle := Bundle{Selection: selection}
	if b.deps.Session != nil && scope.SessionID != "" {
		if session, err := b.deps.Session(ctx, scope.SessionID); err == nil {
			bundle.Session = &session
		}
	}
	if b.deps.Screen != nil && scope.TabID != "" {
		if screen, err := b.deps.Screen(ctx, scope.TabID); err == nil {
			bundle.Screen = screen.Text
			if len(screen.Tail) != 0 {
				bundle.Tail = append([]string(nil), screen.Tail...)
			}
		}
	}
	if b.deps.Tail != nil && scope.TabID != "" {
		if tail, err := b.deps.Tail(ctx, scope.TabID, 80); err == nil {
			bundle.Tail = append([]string(nil), tail...)
		}
	}
	if b.deps.Tables != nil && scope.ConnID != "" {
		if tables, err := b.deps.Tables(ctx, scope.ConnID); err == nil {
			bundle.Tables = tables
		}
	}
	if b.deps.Containers != nil && scope.SessionID != "" {
		if containers, err := b.deps.Containers(ctx, scope.SessionID); err == nil {
			bundle.Containers = containers
		}
	}
	if b.deps.Transport != nil && scope.SessionID != "" {
		bundle.Recon = b.cachedRecon(ctx, scope.SessionID)
	}
	TrimToBudget(&bundle)
	return Prompt{Stable: SystemPrompt(), Volatile: Render(bundle), Bundle: bundle}
}

func SystemPrompt() string {
	return "你是 NexTerm 内置的运维助手，运行在用户的开发运维终端里。\n" +
		"规则：\n" +
		"1. 你执行的每条命令都会记录在右侧对话面板的工具卡片里，用户点开就能看到完整输出。命令不会写进用户的终端区域，所以不要以为「终端里能看到」——需要用户知道的事情，要在回答里说清楚。\n" +
		"2. 工具返回带 exit_code 与 truncated 标记；若返回「连接已断开」，请立即停止排查并提示用户重新连接，不要重试，不要编造结果。\n" +
		"3. 危险命令会被系统拦截：写操作和 sudo 需要用户确认；rm -rf / 等会被直接拒绝。\n" +
		"4. 结论必须带依据：引用你实际执行过的命令与关键输出行。\n" +
		"5. 中文回答。"
}

func PlanPrompt() string {
	return SystemPrompt() + "\n6. 当前为计划模式：只能使用只读工具收集信息，不得执行任何有副作用的操作；完成调查后用 exit_plan_mode 提交清晰、可审核的实施计划，不得自动执行计划。"
}
