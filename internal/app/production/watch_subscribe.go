package production

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// lazySubscriber resolves the watch service when a subscription is made: the
// tool dependencies are built before the watch service exists.
type lazySubscriber struct{ services *ProductionServices }

func (l lazySubscriber) Subscribe(ctx context.Context, conversationID string, subscription tools.Subscription) (tools.SubscriptionResult, error) {
	if l.services == nil || l.services.watches == nil {
		return tools.SubscriptionResult{}, ipc.NewError(ipc.CodeUnsupported, "终端订阅未配置")
	}
	return l.services.watches.Subscribe(ctx, conversationID, subscription)
}

// Subscribe implements tools.TerminalSubscriber: it creates a watch owned by
// the conversation, so its events reach that conversation and wake it.
func (s *watchService) Subscribe(ctx context.Context, conversationID string, subscription tools.Subscription) (tools.SubscriptionResult, error) {
	condition := watches.Condition{Type: watches.ConditionState, Pattern: subscription.On}
	if subscription.On == "output" {
		condition = watches.Condition{Type: watches.ConditionContains, Pattern: subscription.Pattern}
		if subscription.Regex {
			condition.Type = watches.ConditionRegex
		}
	}
	if err := condition.Validate(); err != nil {
		return tools.SubscriptionResult{}, watchIPCError(err)
	}
	executor, err := s.executorFor(ctx, ipc.OwnerID(ctx))
	if err != nil {
		return tools.SubscriptionResult{}, err
	}
	if err := s.ensureAttached(ctx, executor, subscription.TabID); err != nil {
		return tools.SubscriptionResult{}, watchIPCError(err)
	}
	params := watches.WatchParams{
		ConversationID: conversationID, TargetID: subscription.TabID,
		Condition: condition, Repeat: subscription.Repeat, ExpiresAt: subscription.ExpiresAt,
	}
	current, since := s.attentionOf(ctx, subscription.TabID)
	if condition.Type == watches.ConditionState {
		// The state current now is already known to the caller: only a state
		// that begins after this point fires the subscription.
		params.StateSince = since
		if params.StateSince.IsZero() {
			params.StateSince = time.Now()
		}
	}
	watch, err := executor.Watch(ctx, params)
	if err != nil {
		return tools.SubscriptionResult{}, watchIPCError(err)
	}
	return tools.SubscriptionResult{ID: watch.ID, CurrentState: current, ExpiresAt: watch.ExpiresAt}, nil
}

// attentionOf returns the tab's current attention state and when it began.
func (s *watchService) attentionOf(ctx context.Context, tabID string) (string, time.Time) {
	states, err := s.sessions.ListAttentionFor(productionSessionRequestContext(ctx))
	if err != nil {
		return "", time.Time{}
	}
	for _, state := range states {
		if state.TabID != tabID {
			continue
		}
		since, _ := time.Parse(time.RFC3339Nano, state.Since)
		return state.State, since
	}
	return "", time.Time{}
}
