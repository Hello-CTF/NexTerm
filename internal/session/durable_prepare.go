package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// durableFailureRetryWindow is how long a failed helper deployment stays in force before a new one is attempted.
const durableFailureRetryWindow = 30 * time.Second

// Durable readiness states reported by PrepareDurable.
const (
	DurableReady       = "ready"
	DurablePreparing   = "preparing"
	DurableUnavailable = "unavailable"
	// DurableDisabled means the asset or account chose regular terminals, so nothing is prepared.
	DurableDisabled = "disabled"
)

// DurableReadiness reports whether a new terminal on a session can be durable right now.
type DurableReadiness struct {
	State  string
	Reason string
}

// PrepareDurable reports the durable terminal readiness of a connected session and, for SSH sessions whose helper
// is neither ready nor deploying, starts the background helper deployment. It never waits for the deployment.
func (m *Manager) PrepareDurable(ctx context.Context, sessionID string) (DurableReadiness, error) {
	connected, err := m.SessionFor(ctx, sessionID)
	if err != nil {
		return DurableReadiness{}, err
	}
	switch connected.Asset().Kind {
	case KindDevice:
		return DurableReadiness{State: DurableReady}, nil
	case KindSSH:
	case KindLocal:
		return m.localDurableReadiness(ctx, connected)
	default:
		return DurableReadiness{State: DurableUnavailable, Reason: "此类连接不支持持久终端"}, nil
	}
	mode, err := m.terminalModeFor(ctx, connected)
	if err != nil {
		return DurableReadiness{}, err
	}
	if mode == TerminalModeRegular {
		return DurableReadiness{State: DurableDisabled}, nil
	}
	if m.resolver == nil {
		return DurableReadiness{State: DurableUnavailable, Reason: "此环境没有持久终端组件"}, nil
	}
	connected.mu.Lock()
	if connected.closed || connected.status != StatusConnected || connected.transport == nil {
		connected.mu.Unlock()
		return DurableReadiness{State: DurableUnavailable, Reason: "会话未连接"}, nil
	}
	transport, generation := connected.transport, connected.generation
	connected.mu.Unlock()

	// The running deployment is checked before the stored outcome: a finished deployment stores its outcome before
	// it stops being the running one, so a missing entry means the outcome below is already visible.
	if matchingBackgroundHelper(connected, transport, generation) != nil {
		return DurableReadiness{State: DurablePreparing}, nil
	}
	connected.durableMu.Lock()
	current := connected.durableGen == generation && connected.durableTransport == transport
	provider := connected.durableProvider
	createFailure := connected.durableCreateFailure
	failure := connected.durableFailure
	failedAt := connected.durableFailureAt
	connected.durableMu.Unlock()
	if current && provider != nil {
		if createFailure != nil {
			return DurableReadiness{State: DurableUnavailable, Reason: durableReadinessReason(createFailure)}, nil
		}
		return DurableReadiness{State: DurableReady}, nil
	}
	// A failure stays reported for the same retry window the tab path uses; afterwards a fresh deployment starts.
	if current && failure != nil && m.now().Sub(failedAt) < durableFailureRetryWindow {
		return DurableReadiness{State: DurableUnavailable, Reason: durableReadinessReason(failure)}, nil
	}
	if _, _, err := m.ensureBackgroundHelper(connected, transport, generation, true); err != nil {
		if errors.Is(err, ErrDisconnected) || errors.Is(err, ErrStaleGeneration) {
			return DurableReadiness{State: DurableUnavailable, Reason: "会话未连接"}, nil
		}
		return DurableReadiness{State: DurableUnavailable, Reason: durableReadinessReason(err)}, nil
	}
	return DurableReadiness{State: DurablePreparing}, nil
}

func durableReadinessReason(err error) string {
	switch {
	case errors.Is(err, ErrUnsupported), errors.Is(err, base.ErrUnsupported):
		return "目标主机不支持持久终端组件"
	case errors.Is(err, context.Canceled):
		return "持久组件部署已取消"
	// A helper that needs an upgrade is something the user can act on; the raw protocol error is not.
	case errors.Is(err, supervisor.ErrHelperUpgradeRequired), errors.Is(err, supervisor.ErrClientUpgradeRequired):
		return userMessage(err)
	default:
		return err.Error()
	}
}
