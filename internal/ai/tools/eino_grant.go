package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

// SessionAssetTerminal resolves the asset ID of a session on the server side.
// Device-grant identity is derived from the tab/session the chat is scoped to,
// never from the client-supplied scope fields.
type SessionAssetTerminal interface {
	SessionAssetID(context.Context, string) (string, error)
}

// ConnAssetIdentity resolves the real current database asset identity of a
// live connection for owner-scoped grant evaluation. Production wiring
// implements it with db.Service.AssetIDOf: the identity is the DB asset the
// connection was resolved from at connect time; an SSH tunnel viaAssetId is
// transport-only, and inline connections honestly resolve to none.
type ConnAssetIdentity interface {
	AssetIDOf(context.Context, string) (string, bool)
}

// dbGrantCall reports whether the call's grant identity comes from the scope's
// database connection rather than from the session asset.
func dbGrantCall(name string) bool {
	switch name {
	case "db_list_tables", "db_describe", "db_query", "redis_scan":
		return true
	default:
		return false
	}
}

// ResolveGrantDeviceID resolves the server-side asset identity of an
// execution scope: the scope session itself, or the session owning the scope
// tab. Missing server support or resolution failures resolve to empty, which
// disables the device-grant branch and falls back to the global mode.
func ResolveGrantDeviceID(ctx context.Context, registry *Registry, scope Scope) string {
	if registry == nil {
		return ""
	}
	terminal, ok := registry.deps.Terminal.(SessionAssetTerminal)
	if !ok {
		return ""
	}
	sessionID := scope.SessionID
	if sessionID == "" && scope.TabID != "" {
		resolved, err := registry.tabSession(ctx, scope.TabID)
		if err != nil || resolved == "" {
			return ""
		}
		sessionID = resolved
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

func (e *Execution) grantDeviceID(ctx context.Context) string {
	return ResolveGrantDeviceID(ctx, e.Registry, e.Scope)
}

// resourceForCall extracts the action and primary path needed for call authorization: file-writing actions carry a path; other actions match by name only.
func resourceForCall(call Call) guard.Resource {
	resource := guard.Resource{Action: call.Name}
	switch call.Name {
	case "write_file", "edit_file":
		var fields struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(call.Args, &fields) == nil {
			resource.Path = strings.TrimSpace(fields.Path)
		}
	}
	return resource
}

// normalizeWritePathArgs normalizes write_file/edit_file path arguments at the tool entry:
// grant matching and execution use the same normalized path; out-of-bounds paths still containing .. after normalization are rejected outright.
func normalizeWritePathArgs(args json.RawMessage) (json.RawMessage, error) {
	var fields struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &fields); err != nil {
		return args, nil
	}
	cleaned, ok := guard.NormalizePath(fields.Path)
	if !ok {
		return nil, errors.New("路径越界：规范化后仍包含 ..")
	}
	if cleaned == fields.Path {
		return args, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return args, nil
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		return nil, err
	}
	raw["path"] = encoded
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

// persistentRulePattern derives the path scope an "always allow" decision writes into the rule:
// file-writing actions are limited to their directory tree; other actions are not path-limited (whole-device for that action).
func persistentRulePattern(resource guard.Resource) string {
	if guard.RuleActionNeedsPath(resource.Action) {
		return guard.DirPattern(resource.Path)
	}
	return ""
}

// persistPermanentGrant turns an "always allow" decision into a persistent rule; any step that
// cannot safely determine the scope (non-NeedsConfirm ruling, grants not configured, device
// identity unresolvable) is rejected with an error, and the caller treats it as fail-closed
// without executing the operation.
func (e *Execution) persistPermanentGrant(ctx context.Context, state InteractionState) error {
	if state.Info.Risk != "needs_confirm" {
		return errors.New("该操作必须逐次确认，不支持永久授权")
	}
	if e.Registry == nil || e.Registry.deps.Grants == nil {
		return errors.New("永久授权未配置")
	}
	deviceID := e.grantDeviceID(ctx)
	if deviceID == "" {
		return errors.New("无法确定设备身份，不能保存永久授权")
	}
	_, err := e.Registry.deps.Grants.AddRule(ctx, deviceID, state.Resource.Action, persistentRulePattern(state.Resource), time.Time{})
	return err
}

// decide evaluates a ruling for a chat tool call: explicit deny first, then
// the persistent device grant for the enumerated terminal_write/session_exec
// operations, then the global permission mode. A nil grants manager keeps the
// plain global-mode evaluation, so device grants stay off unless configured.
// Database calls take the connection branch: their identity is the real DB
// asset behind the scope's connection, never the session asset.
func (e *Execution) decide(ctx context.Context, call Call, ruling guard.Ruling, memory *guard.Memory) guard.Decision {
	resource := resourceForCall(call)
	if e.Registry == nil || e.Registry.deps.Grants == nil {
		return guard.DecideResource(e.Permission, ruling, memory, resource)
	}
	if dbGrantCall(call.Name) {
		return e.decideDB(ctx, call, ruling, memory, resource)
	}
	return e.Registry.deps.Grants.EvaluateResource(ctx, e.Permission, ruling, memory, e.grantDeviceID(ctx), call.Name, e.JobID, resource)
}

// decideDB evaluates a database tool call against the grants of the asset the
// scope's connection really belongs to. The identity resolves live at use time
// through the server-side connection service: a connection whose asset
// identity cannot be resolved (inline configuration, a stale or cross-owner
// handle, or a backend without identity support) gets no grant branch — the
// previous binding is dropped and the plain permission mode applies, so an
// unbound connId never inherits another asset's grant and an inline
// connection never gains an invented identity.
func (e *Execution) decideDB(ctx context.Context, call Call, ruling guard.Ruling, memory *guard.Memory, resource guard.Resource) guard.Decision {
	grants := e.Registry.deps.Grants
	connID := strings.TrimSpace(e.Scope.ConnID)
	identity, ok := e.Registry.deps.Database.(ConnAssetIdentity)
	if ok && connID != "" {
		if assetID, found := identity.AssetIDOf(ctx, connID); found && strings.TrimSpace(assetID) != "" {
			if err := grants.BindConnAsset(ctx, connID, assetID); err != nil {
				return guard.Decision{Action: guard.ActionDeny, Ruling: ruling, Reason: err.Error()}
			}
			return grants.EvaluateDBResource(ctx, e.Permission, ruling, memory, connID, call.Name, e.JobID, resource)
		}
		grants.UnbindConnAsset(ctx, connID)
	}
	return grants.EvaluateResource(ctx, e.Permission, ruling, memory, "", call.Name, e.JobID, resource)
}
