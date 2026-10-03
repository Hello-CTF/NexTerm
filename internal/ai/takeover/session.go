package takeover

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
)

func WithSession(deps Dependencies, manager *session.Manager, userClientID string) Dependencies {
	terminal := tools.SessionTerminal(manager)
	deps.Snapshot = terminal.Snapshot
	deps.WriteAI = manager.WriteInternal
	deps.WriteUser = func(ctx context.Context, tabID string, data []byte) error {
		return manager.Write(ctx, tabID, userClientID, data)
	}
	deps.Inject = manager.InjectInternal
	deps.TabSession = func(tabID string) string {
		tab, err := manager.Tab(tabID)
		if err != nil {
			return ""
		}
		return tab.SessionID
	}
	return deps
}

func InstallSessionHooks(manager *session.Manager, takeoverManager *Manager) func() {
	manager.SetUserInputHook(takeoverManager.Preempt)
	return func() {
		manager.SetUserInputHook(nil)
	}
}
