package guard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
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
}

type GrantAuditor func(context.Context, GrantAuditEvent) error

type Grants struct {
	settings Settings
	audit    GrantAuditor
	now      func() time.Time

	mu     sync.RWMutex
	grants map[string]DeviceGrant
}

func NewGrants(ctx context.Context, settings Settings, audit GrantAuditor) (*Grants, error) {
	manager := &Grants{settings: settings, audit: audit, now: time.Now, grants: make(map[string]DeviceGrant)}
	raw, found, err := settings.SettingGet(ctx, DeviceGrantsSettingKey)
	if err != nil {
		return nil, err
	}
	if found && raw != "" {
		if err := json.Unmarshal([]byte(raw), &manager.grants); err != nil {
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
	if ruling.Risk == Forbidden {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: ruling.Reason}
	}
	if ruling.Risk == NeedsConfirm && deviceID != "" {
		if grant, ok := g.Get(deviceID); ok && grant.Covers(OperationGrantKind(operation)) {
			g.emitAudit(ctx, GrantAuditEvent{Action: "use", DeviceID: deviceID, Kinds: grant.Kinds, Operation: operation, RunID: runID})
			return Decision{Action: ActionAllow, Ruling: ruling, Reason: "设备长期授权"}
		}
	}
	return Decide(config, ruling, memory)
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
