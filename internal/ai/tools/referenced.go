package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// ReferencedTerminal is one @-referenced tab granted to a job. ReadOnly is
// false when the tab is on the conversation's own connection, where the
// ordinary write rules already apply.
type ReferencedTerminal struct {
	Handle   TerminalHandle
	ReadOnly bool
}

// GrantReferencedTabs records the terminal tabs the user explicitly
// @-referenced in this conversation as read-only targets of the job. Each tab
// is bound to the session it belongs to now, resolved under the caller's own
// access; tabs the caller cannot see or that no longer exist are skipped. The
// terminal read tools exist only in a scope with a tab, so without one
// nothing is granted.
func (r *Registry) GrantReferencedTabs(ctx context.Context, jobID string, scope Scope, tabIDs []string) []ReferencedTerminal {
	if !r.hasTabSessionResolver() || r.deps.Terminal == nil || scope.TabID == "" || len(tabIDs) == 0 {
		return nil
	}
	state := r.state(jobID)
	granted := make([]ReferencedTerminal, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		if tabID == "" {
			continue
		}
		sessionID, err := r.tabSession(ctx, tabID)
		if err != nil || sessionID == "" {
			continue
		}
		state.rememberReferencedTab(tabID, sessionID)
		granted = append(granted, ReferencedTerminal{
			Handle:   TerminalHandle{TabID: tabID, SessionID: sessionID},
			ReadOnly: scope.SessionID == "" || sessionID != scope.SessionID,
		})
	}
	return granted
}

// resolveReadableTab resolves the target of a read-only terminal tool: every
// target resolveTab accepts, plus tabs the user @-referenced in this
// conversation. Writes never use it, because approvals, device grants and
// production checks are resolved from the conversation's own connection.
func (r *Registry) resolveReadableTab(ctx context.Context, jobID string, scope Scope, requested string) (string, error) {
	tabID, err := r.resolveTab(ctx, jobID, scope, requested)
	if !errors.Is(err, ErrCrossScope) {
		return tabID, err
	}
	candidate, handleSession := requested, ""
	if strings.HasPrefix(requested, "t/") {
		handle, parseErr := ParseTerminalHandle(requested)
		if parseErr != nil {
			return "", parseErr
		}
		candidate, handleSession = handle.TabID, handle.SessionID
	}
	bound, ok := r.state(jobID).referencedTab(candidate)
	if !ok {
		return "", err
	}
	if handleSession != "" && handleSession != bound {
		return "", ErrTerminalReplaced
	}
	current, lookupErr := r.tabSession(ctx, candidate)
	if lookupErr != nil {
		if errors.Is(lookupErr, session.ErrTabNotFound) {
			return "", ErrTerminalGone
		}
		return "", lookupErr
	}
	if current == "" {
		return "", ErrTerminalGone
	}
	if current != bound {
		return "", ErrTerminalReplaced
	}
	if r.deps.Terminal == nil {
		return "", errors.New("终端服务未配置")
	}
	return candidate, nil
}

// readOnlyTerminalTool reports the terminal tools that resolve targets with
// resolveReadableTab.
func readOnlyTerminalTool(name string) bool {
	return name == "read_screen" || name == "wait_for"
}
