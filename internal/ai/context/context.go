package aicontext

import (
	"context"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
	Now        time.Time
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
	TabSession func(string) string
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
	recon map[reconCacheKey]*reconEntry
}

type reconCacheKey struct {
	sessionID  string
	ownerScope string
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
	return &Builder{deps: deps, recon: make(map[reconCacheKey]*reconEntry)}
}

type Prompt struct {
	Stable   string
	Volatile string
	Bundle   Bundle
}

func (b *Builder) Build(ctx context.Context, scope tools.Scope, selection string) Prompt {
	bundle := Bundle{Selection: selection, Now: b.deps.Now()}
	if b.deps.Session != nil && scope.SessionID != "" {
		if session, err := b.deps.Session(ctx, scope.SessionID); err == nil {
			bundle.Session = &session
		}
	}
	tabAllowed := scope.TabID != ""
	var screen tools.Screen
	screenRead := false
	_, authenticated := ipc.UserIDFromContext(ctx)
	if tabAllowed && authenticated && b.deps.TabSession != nil {
		if b.deps.Screen == nil {
			tabAllowed = false
		} else {
			var err error
			screen, err = b.deps.Screen(ctx, scope.TabID)
			screenRead = true
			tabAllowed = err == nil
		}
	}
	if tabAllowed && scope.SessionID != "" && b.deps.TabSession != nil {
		tabAllowed = b.deps.TabSession(scope.TabID) == scope.SessionID
	}
	if b.deps.Screen != nil && tabAllowed {
		if !screenRead {
			var err error
			screen, err = b.deps.Screen(ctx, scope.TabID)
			screenRead = true
			tabAllowed = err == nil
		}
		if tabAllowed {
			bundle.Screen = screen.Text
			if len(screen.Tail) != 0 {
				bundle.Tail = append([]string(nil), screen.Tail...)
			}
		}
	}
	if b.deps.Tail != nil && tabAllowed {
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
	return "You are the ops assistant built into NexTerm, running inside the user's development and operations terminal.\n" +
		"Rules:\n" +
		"1. Every command you run is recorded in a tool card in the conversation panel on the right; the user can open it to see the full output. Commands are not written into the user's terminal area, so do not assume \"the terminal shows it\" - anything the user needs to know must be stated clearly in your reply.\n" +
		"2. Tool results carry exit_code and truncated flags; if a result says \"disconnected\", stop troubleshooting immediately and ask the user to reconnect; do not retry, do not fabricate results.\n" +
		"3. Dangerous commands are intercepted by the system: write operations and sudo require user confirmation; commands like rm -rf / are rejected outright.\n" +
		"4. Conclusions must be evidence-based: cite the commands you actually ran and the key output lines.\n" +
		"5. When a long task completes or something needs the user's immediate attention, send an in-app notification with the notify tool; the notification content must also be written into the current reply body, because the popup may be missed.\n" +
		"6. Answer in Chinese."
}

func PlanPrompt() string {
	return SystemPrompt() + "\n7. Plan mode is active: use only read-only tools to gather information; do not perform any operation with side effects; when the investigation is complete, submit a clear, reviewable implementation plan with exit_plan_mode, and do not execute the plan automatically."
}
