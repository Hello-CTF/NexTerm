package guard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const DeviceGrantsSettingKey = "ai.device_grants"

type GrantKind string

const (
	GrantKindTerminalWrite GrantKind = "terminal_write"
	GrantKindSessionExec   GrantKind = "session_exec"
)

type DeviceGrant struct {
	DeviceID  string      `json:"deviceId"`
	Kinds     []GrantKind `json:"kinds"`
	UpdatedAt int64       `json:"updatedAt"`
}

func ParseGrantKinds(values []string) ([]GrantKind, error) {
	if len(values) == 0 {
		return nil, errors.New("设备授权至少需要一个种类")
	}
	seen := make(map[GrantKind]struct{}, len(values))
	kinds := make([]GrantKind, 0, len(values))
	for _, value := range values {
		kind := GrantKind(strings.TrimSpace(value))
		switch kind {
		case GrantKindTerminalWrite, GrantKindSessionExec:
		default:
			return nil, errors.New("设备授权种类仅支持 terminal_write/session_exec")
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func (g DeviceGrant) Covers(kind GrantKind) bool {
	if kind == "" {
		return false
	}
	for _, granted := range g.Kinds {
		if granted == kind {
			return true
		}
	}
	return false
}

func OperationGrantKind(operation string) GrantKind {
	switch operation {
	case "send_keys":
		return GrantKindTerminalWrite
	case "exec_commands":
		return GrantKindSessionExec
	default:
		return ""
	}
}

type GrantAuditEvent struct {
	Action    string      `json:"action"`
	DeviceID  string      `json:"deviceId"`
	Kinds     []GrantKind `json:"kinds,omitempty"`
	Operation string      `json:"operation,omitempty"`
	RunID     string      `json:"runId,omitempty"`
	RuleID    string      `json:"ruleId,omitempty"`
	RulePath  string      `json:"rulePath,omitempty"`
}

type GrantAuditor func(context.Context, GrantAuditEvent) error

type Grants struct {
	settings Settings
	audit    GrantAuditor
	now      func() time.Time
	newID    func() string

	mu     sync.RWMutex
	grants map[string]DeviceGrant
	rules  []GrantRule
}

func NewGrants(ctx context.Context, settings Settings, audit GrantAuditor) (*Grants, error) {
	manager := &Grants{settings: settings, audit: audit, now: time.Now, newID: ids.New, grants: make(map[string]DeviceGrant)}
	raw, found, err := settings.SettingGet(ctx, DeviceGrantsSettingKey)
	if err != nil {
		return nil, err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &manager.grants); err != nil {
			return nil, err
		}
	}
	raw, found, err = settings.SettingGet(ctx, GrantRulesSettingKey)
	if err != nil {
		return nil, err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &manager.rules); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func (g *Grants) Grant(ctx context.Context, deviceID string, kinds []GrantKind) (DeviceGrant, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return DeviceGrant{}, errors.New("设备授权缺少设备 ID")
	}
	if len(kinds) == 0 {
		return DeviceGrant{}, errors.New("设备授权至少需要一个种类")
	}
	seen := make(map[GrantKind]struct{}, len(kinds))
	normalized := make([]GrantKind, 0, len(kinds))
	for _, kind := range kinds {
		parsed, err := ParseGrantKinds([]string{string(kind)})
		if err != nil {
			return DeviceGrant{}, err
		}
		if _, ok := seen[parsed[0]]; ok {
			continue
		}
		seen[parsed[0]] = struct{}{}
		normalized = append(normalized, parsed[0])
	}
	grant := DeviceGrant{DeviceID: deviceID, Kinds: normalized, UpdatedAt: g.now().UnixMilli()}
	g.mu.Lock()
	previous, existed := g.grants[deviceID]
	g.grants[deviceID] = grant
	err := g.persistLocked(ctx)
	if err != nil {
		if existed {
			g.grants[deviceID] = previous
		} else {
			delete(g.grants, deviceID)
		}
	}
	g.mu.Unlock()
	if err != nil {
		return DeviceGrant{}, err
	}
	g.emitAudit(ctx, GrantAuditEvent{Action: "grant", DeviceID: deviceID, Kinds: normalized})
	return grant, nil
}

func (g *Grants) Revoke(ctx context.Context, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("设备授权缺少设备 ID")
	}
	g.mu.Lock()
	previous, ok := g.grants[deviceID]
	if !ok {
		g.mu.Unlock()
		return nil
	}
	delete(g.grants, deviceID)
	err := g.persistLocked(ctx)
	if err != nil {
		g.grants[deviceID] = previous
	}
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.emitAudit(ctx, GrantAuditEvent{Action: "revoke", DeviceID: deviceID})
	return nil
}

// AddRule 新增一条路径级授权规则；同设备同动作同范围的已有规则（含已过期）会被更新而不是重复添加。
// expiresAt 为零值表示永久授权。
func (g *Grants) AddRule(ctx context.Context, deviceID, action, pattern string, expiresAt time.Time) (GrantRule, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return GrantRule{}, errors.New("授权规则缺少设备 ID")
	}
	if !RuleActionSupported(action) {
		return GrantRule{}, errors.New("授权规则动作仅支持 write_file/edit_file/exec_commands/send_keys")
	}
	pattern = strings.TrimSpace(pattern)
	if RuleActionNeedsPath(action) && pattern == "" {
		return GrantRule{}, errors.New("文件写入规则必须限定路径范围")
	}
	if !RuleActionNeedsPath(action) && pattern != "" {
		return GrantRule{}, errors.New("该动作的规则不支持路径范围")
	}
	var expiresAtMS int64
	if !expiresAt.IsZero() {
		expiresAtMS = expiresAt.UnixMilli()
		if expiresAtMS <= g.now().UnixMilli() {
			return GrantRule{}, errors.New("授权规则过期时间必须晚于当前时间")
		}
	}
	g.mu.Lock()
	now := g.now().UnixMilli()
	for index, existing := range g.rules {
		if existing.DeviceID != deviceID || existing.Action != action || existing.Path != pattern {
			continue
		}
		previous := existing
		existing.ExpiresAt = expiresAtMS
		existing.UpdatedAt = now
		g.rules[index] = existing
		if err := g.persistRulesLocked(ctx); err != nil {
			g.rules[index] = previous
			g.mu.Unlock()
			return GrantRule{}, err
		}
		g.mu.Unlock()
		g.emitAudit(ctx, GrantAuditEvent{Action: "rule_grant", DeviceID: deviceID, Operation: action, RuleID: existing.ID, RulePath: pattern})
		return existing, nil
	}
	rule := GrantRule{ID: g.newID(), DeviceID: deviceID, Action: action, Path: pattern, ExpiresAt: expiresAtMS, CreatedAt: now, UpdatedAt: now}
	g.rules = append(g.rules, rule)
	if err := g.persistRulesLocked(ctx); err != nil {
		g.rules = g.rules[:len(g.rules)-1]
		g.mu.Unlock()
		return GrantRule{}, err
	}
	g.mu.Unlock()
	g.emitAudit(ctx, GrantAuditEvent{Action: "rule_grant", DeviceID: deviceID, Operation: action, RuleID: rule.ID, RulePath: pattern})
	return rule, nil
}

// RevokeRule 按 ID 撤销一条授权规则；对未知 ID 幂等成功。
func (g *Grants) RevokeRule(ctx context.Context, ruleID string) error {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return errors.New("授权规则缺少规则 ID")
	}
	g.mu.Lock()
	index := -1
	var previous GrantRule
	for i, rule := range g.rules {
		if rule.ID == ruleID {
			index = i
			previous = rule
			break
		}
	}
	if index < 0 {
		g.mu.Unlock()
		return nil
	}
	g.rules = append(g.rules[:index], g.rules[index+1:]...)
	err := g.persistRulesLocked(ctx)
	if err != nil {
		g.rules = append(g.rules, previous)
	}
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.emitAudit(ctx, GrantAuditEvent{Action: "rule_revoke", DeviceID: previous.DeviceID, Operation: previous.Action, RuleID: previous.ID, RulePath: previous.Path})
	return nil
}

// Rules 返回全部授权规则（含已过期规则，由展示层标注或惰性清理）。
func (g *Grants) Rules() []GrantRule {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]GrantRule, len(g.rules))
	copy(result, g.rules)
	return result
}

// matchRule 返回覆盖给定动作与路径的未过期规则。
func (g *Grants) matchRule(deviceID, action, path string) (GrantRule, bool) {
	now := g.now()
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, rule := range g.rules {
		if rule.DeviceID == deviceID && !rule.Expired(now) && rule.Covers(action, path) {
			return rule, true
		}
	}
	return GrantRule{}, false
}

func (g *Grants) persistRulesLocked(ctx context.Context) error {
	encoded, err := json.Marshal(g.rules)
	if err != nil {
		return err
	}
	return g.settings.SettingSet(ctx, GrantRulesSettingKey, string(encoded))
}

func (g *Grants) Get(deviceID string) (DeviceGrant, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	grant, ok := g.grants[deviceID]
	if !ok {
		return DeviceGrant{}, false
	}
	grant.Kinds = append([]GrantKind(nil), grant.Kinds...)
	return grant, true
}

func (g *Grants) List() []DeviceGrant {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]DeviceGrant, 0, len(g.grants))
	for _, grant := range g.grants {
		grant.Kinds = append([]GrantKind(nil), grant.Kinds...)
		result = append(result, grant)
	}
	return result
}

func (g *Grants) Evaluate(ctx context.Context, config Config, ruling Ruling, memory *Memory, deviceID, operation, runID string) Decision {
	return g.EvaluateResource(ctx, config, ruling, memory, deviceID, operation, runID, Resource{})
}

// EvaluateResource 在 Evaluate 之上叠加路径级规则：NeedsConfirm 裁定先匹配持久规则，
// 再匹配本会话记住的路径规则，最后回落到设备级授权与全局权限模式。
// Forbidden 始终拒绝；Danger 与 Unknowable 不会被任何授权覆盖，保持逐次确认。
func (g *Grants) EvaluateResource(ctx context.Context, config Config, ruling Ruling, memory *Memory, deviceID, operation, runID string, resource Resource) Decision {
	if ruling.Risk == Forbidden {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: ruling.Reason}
	}
	if ruling.Risk == NeedsConfirm {
		action := resource.Action
		if action == "" {
			action = operation
		}
		if deviceID != "" {
			if rule, ok := g.matchRule(deviceID, action, resource.Path); ok {
				g.emitAudit(ctx, GrantAuditEvent{Action: "use_rule", DeviceID: deviceID, Operation: action, RunID: runID, RuleID: rule.ID, RulePath: rule.Path})
				return Decision{Action: ActionAllow, Ruling: ruling, Reason: "路径规则授权"}
			}
			if grant, ok := g.Get(deviceID); ok && grant.Covers(OperationGrantKind(operation)) {
				g.emitAudit(ctx, GrantAuditEvent{Action: "use", DeviceID: deviceID, Kinds: grant.Kinds, Operation: operation, RunID: runID})
				return Decision{Action: ActionAllow, Ruling: ruling, Reason: "设备长期授权"}
			}
		}
	}
	return DecideResource(config, ruling, memory, resource)
}

func (g *Grants) persistLocked(ctx context.Context) error {
	encoded, err := json.Marshal(g.grants)
	if err != nil {
		return err
	}
	return g.settings.SettingSet(ctx, DeviceGrantsSettingKey, string(encoded))
}

func (g *Grants) emitAudit(ctx context.Context, event GrantAuditEvent) {
	if g.audit != nil {
		_ = g.audit(ctx, event)
	}
}
