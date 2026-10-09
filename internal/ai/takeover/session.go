package takeover

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func WithSession(deps Dependencies, manager *session.Manager) Dependencies {
	terminal := tools.SessionTerminal(manager)
	deps.Snapshot = terminal.Snapshot
	deps.WriteAI = manager.WriteInternal
	deps.Inject = manager.InjectInternal
	deps.TabSession = func(tabID string) string {
		tab, err := manager.Tab(tabID)
		if err != nil {
			return ""
		}
		return tab.SessionID
	}
	deps.TabAsset = func(ctx context.Context, tabID string) (string, error) {
		tab, err := manager.Tab(tabID)
		if err != nil {
			return "", err
		}
		sess, err := manager.Session(tab.SessionID)
		if err != nil {
			return "", err
		}
		return sess.Asset().ID, nil
	}
	return deps
}

func InstallSessionHooks(manager *session.Manager, takeoverManager *Manager) func() {
	manager.SetUserInputHook(takeoverManager.Pause)
	return func() {
		manager.SetUserInputHook(nil)
	}
}
