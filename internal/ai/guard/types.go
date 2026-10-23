package guard

import (
	"strings"
	"sync"
)

type Mode string

const (
	ReadOnly   Mode = "read_only"
	ReadWrite  Mode = "read_write"
	Silent     Mode = "silent"
	Unattended Mode = "unattended"
)

type Risk uint8

const (
	Safe Risk = iota
	NeedsConfirm
	Unknowable
	Danger
	Forbidden
)

func (r Risk) String() string {
	switch r {
	case Safe:
		return "safe"
	case NeedsConfirm:
		return "needs_confirm"
	case Unknowable:
		return "unknowable"
	case Danger:
		return "danger"
	case Forbidden:
		return "forbidden"
	default:
		return "forbidden"
	}
}

type Kind string

const (
	KindNone         Kind = ""
	KindUnknown      Kind = "unknown"
	KindSudo         Kind = "sudo"
	KindWriteFS      Kind = "write_fs"
	KindService      Kind = "service"
	KindDockerMutate Kind = "docker_mutate"
	KindPackage      Kind = "package"
	KindProcess      Kind = "process"
	KindDBWrite      Kind = "db_write"
	KindShellPipe    Kind = "shell_pipe"
	KindSchedule     Kind = "schedule"
	KindPermission   Kind = "perm"
	KindDanger       Kind = "danger"
	// KindProgram marks a command NexTerm has no rule for; Ruling.Programs names
	// the programs, so a session approval covers exactly those programs.
	KindProgram Kind = "program"
)

type Ruling struct {
	Risk     Risk     `json:"risk"`
	Kind     Kind     `json:"kind"`
	Kinds    []Kind   `json:"kinds,omitempty"`
	Programs []string `json:"programs,omitempty"`
	Reason   string   `json:"reason"`
}

func Allow(reason ...string) Ruling {
	return Ruling{Risk: Safe, Reason: first(reason)}
}

func Confirm(kind Kind, reason string) Ruling {
	return Ruling{Risk: NeedsConfirm, Kind: kind, Kinds: []Kind{kind}, Reason: reason}
}

// ConfirmProgram asks before running a program NexTerm has no rule for.
func ConfirmProgram(name, reason string) Ruling {
	return Ruling{Risk: NeedsConfirm, Kind: KindProgram, Kinds: []Kind{KindProgram}, Programs: []string{name}, Reason: reason}
}

func Indeterminate(reason string) Ruling {
	return Ruling{Risk: Unknowable, Kind: KindUnknown, Kinds: []Kind{KindUnknown}, Reason: reason}
}

func Dangerous(reason string) Ruling {
	return Ruling{Risk: Danger, Kind: KindDanger, Kinds: []Kind{KindDanger}, Reason: reason}
}

func Deny(reason string) Ruling {
	return Ruling{Risk: Forbidden, Kind: KindDanger, Kinds: []Kind{KindDanger}, Reason: reason}
}

func Worst(rulings ...Ruling) Ruling {
	result := Allow()
	for _, ruling := range rulings {
		if ruling.Risk > result.Risk || (ruling.Risk == result.Risk && result.Reason == "") {
			result = ruling
		}
	}
	result.Kinds = nil
	result.Programs = nil
	for _, ruling := range rulings {
		if ruling.Risk != result.Risk {
			continue
		}
		for _, kind := range ruling.ApprovalKinds() {
			seen := false
			for _, existing := range result.Kinds {
				seen = seen || existing == kind
			}
			if !seen {
				result.Kinds = append(result.Kinds, kind)
			}
		}
		for _, program := range ruling.Programs {
			seen := false
			for _, existing := range result.Programs {
				seen = seen || existing == program
			}
			if !seen {
				result.Programs = append(result.Programs, program)
			}
		}
	}
	return result
}

func (r Ruling) ApprovalKinds() []Kind {
	if len(r.Kinds) != 0 {
		return append([]Kind(nil), r.Kinds...)
	}
	if r.Kind != KindNone {
		return []Kind{r.Kind}
	}
	return nil
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type Config struct {
	Mode        Mode     `json:"mode"`
	DangerRules []string `json:"dangerRules"`
}

func (c Config) Normalized() Config {
	if c.Mode != ReadOnly && c.Mode != ReadWrite && c.Mode != Silent && c.Mode != Unattended {
		c.Mode = ReadWrite
	}
	seen := make(map[string]struct{}, len(c.DangerRules))
	rules := make([]string, 0, len(c.DangerRules))
	for _, rule := range c.DangerRules {
		rule = strings.TrimSpace(rule)
		key := strings.ToLower(rule)
		if rule == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		rules = append(rules, rule)
	}
	c.DangerRules = rules
	return c
}

type Action string

const (
	ActionAllow Action = "allow"
	ActionAsk   Action = "ask"
	ActionDeny  Action = "deny"
)

// AuthorizationBinding identifies the exact automatic grant or rule generation
// that authorized a call. A new mutation never reuses a prior generation.
type AuthorizationBinding struct {
	Kind       string `json:"kind"`
	DeviceID   string `json:"deviceId"`
	RuleID     string `json:"ruleId,omitempty"`
	Generation string `json:"generation"`
}

type Decision struct {
	Action        Action                `json:"action"`
	Ruling        Ruling                `json:"ruling"`
	Reason        string                `json:"reason,omitempty"`
	Authorization *AuthorizationBinding `json:"authorization,omitempty"`
	// Production reports a production target, which session approvals never cover.
	Production bool `json:"production,omitempty"`
}

// DecideResource layers the path-level grants remembered for this session on top of Decide.
// Production targets are never auto-authorized from session memory: the remembered kinds
// and path rules are ignored so every production operation is explicitly confirmed.
func DecideResource(config Config, ruling Ruling, memory *Memory, resource Resource) Decision {
	production := resource.ProductionTarget()
	if production {
		memory = nil
	}
	decision := Decide(config, ruling, memory)
	if ruling.Risk == NeedsConfirm && memory != nil && memory.CoversPathRule(resource.Action, resource.Path) {
		decision = Decision{Action: ActionAllow, Ruling: ruling, Reason: "本会话路径授权"}
	}
	decision.Production = production
	return decision
}

var kindLabels = map[Kind]string{
	KindSudo:         "sudo 提权",
	KindWriteFS:      "文件写入",
	KindService:      "服务与系统设置变更",
	KindDockerMutate: "Docker 容器变更",
	KindPackage:      "软件包安装与卸载",
	KindProcess:      "新建终端",
	KindDBWrite:      "数据库写入",
	KindShellPipe:    "管道执行",
	KindSchedule:     "定时提醒",
	KindPermission:   "权限与属主变更",
	"shell_history":  "读取 shell 历史",
}

// SessionScope describes what a session approval of the ruling covers, for the
// confirmation card; it is empty when a session approval is not offered.
func SessionScope(ruling Ruling) string {
	if !Rememberable(ruling) {
		return ""
	}
	parts := make([]string, 0, len(ruling.ApprovalKinds()))
	for _, kind := range ruling.ApprovalKinds() {
		if kind == KindProgram {
			parts = append(parts, strings.Join(ruling.Programs, "、")+" 命令")
			continue
		}
		label, ok := kindLabels[kind]
		if !ok {
			return ""
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "、")
}

func Decide(config Config, ruling Ruling, memory *Memory) Decision {
	config = config.Normalized()
	if ruling.Risk == Forbidden {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: ruling.Reason}
	}
	if ruling.Risk == Safe {
		return Decision{Action: ActionAllow, Ruling: ruling}
	}
	if config.Mode == Unattended {
		ruling.Reason = unattendedReason(ruling)
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: ruling.Reason}
	}
	if config.Mode == ReadOnly {
		return Decision{Action: ActionDeny, Ruling: ruling, Reason: "只读权限模式禁止此操作"}
	}
	if ruling.Risk == NeedsConfirm {
		if memory.Covers(ruling) {
			return Decision{Action: ActionAllow, Ruling: ruling}
		}
		if config.Mode == Silent {
			return Decision{Action: ActionAllow, Ruling: ruling}
		}
	}
	return Decision{Action: ActionAsk, Ruling: ruling, Reason: ruling.Reason}
}

type Memory struct {
	mu       sync.RWMutex
	kinds    map[Kind]struct{}
	programs map[string]struct{}
	rules    []pathRule
}

type pathRule struct {
	action  string
	pattern string
}

func NewMemory() *Memory {
	return &Memory{kinds: make(map[Kind]struct{}), programs: make(map[string]struct{})}
}

// Rememberable reports whether a session approval can cover the ruling: only
// ordinary confirmations whose every kind can be remembered qualify.
func Rememberable(ruling Ruling) bool {
	if ruling.Risk != NeedsConfirm {
		return false
	}
	kinds := ruling.ApprovalKinds()
	if len(kinds) == 0 {
		return false
	}
	for _, kind := range kinds {
		switch kind {
		case KindNone, KindDanger, KindUnknown:
			return false
		case KindProgram:
			if len(ruling.Programs) == 0 {
				return false
			}
		}
	}
	return true
}

// Remember records a session approval for every kind and program of the ruling.
func (m *Memory) Remember(ruling Ruling) {
	if m == nil || !Rememberable(ruling) {
		return
	}
	for _, kind := range ruling.ApprovalKinds() {
		m.Add(kind)
	}
	m.mu.Lock()
	for _, program := range ruling.Programs {
		m.programs[program] = struct{}{}
	}
	m.mu.Unlock()
}

// Covers reports whether earlier session approvals cover every kind and program of the ruling.
func (m *Memory) Covers(ruling Ruling) bool {
	if m == nil || !Rememberable(ruling) {
		return false
	}
	for _, kind := range ruling.ApprovalKinds() {
		if kind != KindProgram && !m.Contains(kind) {
			return false
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, program := range ruling.Programs {
		if _, ok := m.programs[program]; !ok {
			return false
		}
	}
	return true
}

func (m *Memory) Add(kind Kind) {
	if m == nil || kind == KindNone || kind == KindDanger || kind == KindUnknown || kind == KindProgram {
		return
	}
	m.mu.Lock()
	m.kinds[kind] = struct{}{}
	m.mu.Unlock()
}

func (m *Memory) Contains(kind Kind) bool {
	if m == nil || kind == KindNone || kind == KindDanger || kind == KindUnknown || kind == KindProgram {
		return false
	}
	m.mu.RLock()
	_, ok := m.kinds[kind]
	m.mu.RUnlock()
	return ok
}

// AddPathRule remembers a path-level grant valid for this session (e.g. writes under a directory).
func (m *Memory) AddPathRule(action, pattern string) {
	if m == nil || action == "" {
		return
	}
	m.mu.Lock()
	for _, existing := range m.rules {
		if existing.action == action && existing.pattern == pattern {
			m.mu.Unlock()
			return
		}
	}
	m.rules = append(m.rules, pathRule{action: action, pattern: pattern})
	m.mu.Unlock()
}

// CoversPathRule reports whether this session has remembered a grant covering the action and path.
func (m *Memory) CoversPathRule(action, path string) bool {
	if m == nil || action == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rule := range m.rules {
		if rule.action == action && MatchPath(rule.pattern, path) {
			return true
		}
	}
	return false
}
