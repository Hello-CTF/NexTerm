package tools

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
)

// SessionAssetTerminal resolves the asset ID of a session on the server side.
// Device-grant identity is derived from the tab/session the chat is scoped to,
// never from the client-supplied scope fields.
type SessionAssetTerminal interface {
	SessionAssetID(context.Context, string) (string, error)
}

// grantDeviceID resolves the server-side asset identity of the execution
// scope: the scope session itself, or the session owning the scope tab.
// Missing server support or resolution failures resolve to empty, which
// disables the device-grant branch and falls back to the global mode.
func (e *Execution) grantDeviceID(ctx context.Context) string {
	if e.Registry == nil {
		return ""
	}
	terminal, ok := e.Registry.deps.Terminal.(SessionAssetTerminal)
	if !ok {
		return ""
	}
	sessionID := e.Scope.SessionID
	if sessionID == "" && e.Scope.TabID != "" && e.Registry.deps.TabSession != nil {
		sessionID = e.Registry.deps.TabSession(e.Scope.TabID)
	}
	if sessionID == "" {
		return ""
	}
	assetID, err := terminal.SessionAssetID(ctx, sessionID)
	if err != nil {
		return ""
	}
	return assetID
}

// decide evaluates a ruling for a chat tool call: explicit deny first, then
// the persistent device grant for the enumerated terminal_write/session_exec
// operations, then the global permission mode. A nil grants manager keeps the
// plain global-mode evaluation, so device grants stay off unless configured.
func (e *Execution) decide(ctx context.Context, call Call, ruling guard.Ruling, memory *guard.Memory) guard.Decision {
	if e.Registry == nil || e.Registry.deps.Grants == nil {
		return guard.Decide(e.Permission, ruling, memory)
	}
	return e.Registry.deps.Grants.Evaluate(ctx, e.Permission, ruling, memory, e.grantDeviceID(ctx), call.Name, e.JobID)
}
