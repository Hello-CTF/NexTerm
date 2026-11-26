package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	SubscribeTool = "terminal_subscribe"

	subscribeDefaultExpiry = 60 * time.Minute
	subscribeMaxExpiry     = 24 * time.Hour
	subscribePatternMax    = 1024
)

// Subscription asks for a wake-up when a terminal reaches a state or prints
// matching output. On is needs_me, just_finished, exited or output.
type Subscription struct {
	TabID     string
	On        string
	Pattern   string
	Regex     bool
	Repeat    bool
	ExpiresAt time.Time
}

type SubscriptionResult struct {
	ID string
	// CurrentState is the tab's attention state at creation; when it
	// already equals the requested state that state does not fire the
	// subscription, so the caller must act on it now.
	CurrentState string
	ExpiresAt    time.Time
}

// TerminalSubscriber creates subscriptions bound to a conversation.
type TerminalSubscriber interface {
	Subscribe(ctx context.Context, conversationID string, subscription Subscription) (SubscriptionResult, error)
}

type TerminalSubscribeArgs struct {
	TabID            string `json:"tab_id,omitempty" jsonschema_description:"Terminal tab id or handle (default the current tab)"`
	On               string `json:"on" jsonschema:"required,enum=needs_me,enum=finished,enum=exited,enum=output" jsonschema_description:"needs_me: the terminal waits for input; finished: a command just finished; exited: the shell exited; output: printed output matches pattern"`
	Pattern          string `json:"pattern,omitempty" jsonschema_description:"Text to wait for, required with on=output and not allowed otherwise"`
	Regex            bool   `json:"regex,omitempty" jsonschema_description:"Treat pattern as a regular expression"`
	Repeat           bool   `json:"repeat,omitempty" jsonschema_description:"Keep the subscription after it fires, at most once every 30 seconds and for at most 50 fires (default: fire once)"`
	ExpiresInMinutes int    `json:"expires_in_minutes,omitempty" jsonschema_description:"Minutes until the subscription expires (1-1440, default 60)"`
}

var subscribeStates = map[string]string{"needs_me": "needs_me", "finished": "just_finished", "exited": "exited"}

func ValidateSubscribeArgs(now time.Time, args TerminalSubscribeArgs) (Subscription, error) {
	on := strings.TrimSpace(args.On)
	pattern := args.Pattern
	subscription := Subscription{TabID: strings.TrimSpace(args.TabID), Repeat: args.Repeat}
	if state, ok := subscribeStates[on]; ok {
		if pattern != "" || args.Regex {
			return Subscription{}, invalid("on=%s 不接受 pattern 或 regex", on)
		}
		subscription.On = state
	} else if on == "output" {
		if pattern == "" {
			return Subscription{}, invalid("on=output 需要 pattern")
		}
		if !utf8.ValidString(pattern) || len(pattern) > subscribePatternMax {
			return Subscription{}, invalid("pattern 过长或不是有效 UTF-8（上限 %d 字节）", subscribePatternMax)
		}
		subscription.On, subscription.Pattern, subscription.Regex = on, pattern, args.Regex
	} else {
		return Subscription{}, invalid("on 必须是 needs_me、finished、exited 或 output")
	}
	expiry := subscribeDefaultExpiry
	if args.ExpiresInMinutes != 0 {
		if args.ExpiresInMinutes < 1 || time.Duration(args.ExpiresInMinutes)*time.Minute > subscribeMaxExpiry {
			return Subscription{}, invalid("expires_in_minutes 必须在 1 到 %d 之间", int(subscribeMaxExpiry/time.Minute))
		}
		expiry = time.Duration(args.ExpiresInMinutes) * time.Minute
	}
	subscription.ExpiresAt = now.Add(expiry)
	return subscription, nil
}

func (e *Execution) executeSubscribe(ctx context.Context, call Call) Output {
	var args TerminalSubscribeArgs
	if err := decode(call.Args, &args); err != nil {
		return Fail(err)
	}
	subscription, err := ValidateSubscribeArgs(e.Registry.deps.now(), args)
	if err != nil {
		return Fail(err)
	}
	if e.ConversationID == "" {
		return Fail(errors.New("当前运行缺少会话标识，无法创建订阅"))
	}
	tabID, err := e.Registry.resolveTab(ctx, e.JobID, e.Scope, subscription.TabID)
	if err != nil {
		return Fail(err)
	}
	subscription.TabID = tabID
	result, err := e.Registry.deps.Subscriptions.Subscribe(ctx, e.ConversationID, subscription)
	if err != nil {
		return Fail(fmt.Errorf("创建终端订阅失败: %w", err))
	}
	if e.Registry.deps.Audit != nil {
		entry := AuditEntry{Kind: "exec", Payload: map[string]any{"tool": call.Name, "args": redactArguments(call.Args), "run_id": e.JobID, "call_id": call.ID}}
		_ = e.Registry.deps.Audit(context.WithoutCancel(ctx), entry)
	}
	text := fmt.Sprintf("Subscription %s on tab %s is armed until %s. When it fires, a watch_event tool result is added to this conversation and an unattended run starts (rate limited; actions needing confirmation are denied in it).",
		result.ID, tabID, result.ExpiresAt.Format(time.RFC3339))
	if subscription.Repeat {
		text += fmt.Sprintf(" It repeats: after each fire it skips output and state changes for %d seconds, and it ends after %d fires.",
			int(store.AgentWatchRepeatMinInterval/time.Second), store.AgentWatchMaxRepeatFires)
	}
	if result.CurrentState != "" && result.CurrentState == subscription.On {
		text += " The tab is already in state " + result.CurrentState + " now; that state will not fire the subscription, so act on it now."
	}
	return OK(text)
}
