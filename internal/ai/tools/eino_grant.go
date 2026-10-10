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
	if sessionID == "" && scope.TabID != "" && registry.deps.TabSession != nil {
		sessionID = registry.deps.TabSession(scope.TabID)
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

// resourceForCall 提取调用授权所需的动作与主路径：文件写入类动作带路径，其余动作仅按名匹配。
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

// normalizeWritePathArgs 在工具入口规范化 write_file/edit_file 的路径参数：
// 授权匹配与执行使用同一规范化路径；规范化后仍含 .. 的越界路径直接拒绝。
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

// persistentRulePattern 推导「永久允许」将写入规则的路径范围：
// 文件写入类限定到所在目录树，其余动作不限路径（整设备该动作）。
func persistentRulePattern(resource guard.Resource) string {
	if guard.RuleActionNeedsPath(resource.Action) {
		return guard.DirPattern(resource.Path)
	}
	return ""
}

// persistPermanentGrant 把「永久允许」决策落成持久规则；任何一步无法安全确定
// 范围（非 NeedsConfirm 裁定、未配置授权、设备身份不可解析）都拒绝并返回错误，
// 调用方按失败关闭处理，不会执行操作。
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
func (e *Execution) decide(ctx context.Context, call Call, ruling guard.Ruling, memory *guard.Memory) guard.Decision {
	resource := resourceForCall(call)
	if e.Registry == nil || e.Registry.deps.Grants == nil {
		return guard.DecideResource(e.Permission, ruling, memory, resource)
	}
	return e.Registry.deps.Grants.EvaluateResource(ctx, e.Permission, ruling, memory, e.grantDeviceID(ctx), call.Name, e.JobID, resource)
}
