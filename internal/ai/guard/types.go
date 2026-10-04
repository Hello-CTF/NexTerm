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
	KindPermission   Kind = "perm"
	KindDanger       Kind = "danger"
)

type Ruling struct {
	Risk   Risk   `json:"risk"`
	Kind   Kind   `json:"kind"`
	Kinds  []Kind `json:"kinds,omitempty"`
	Reason string `json:"reason"`
}

func Allow(reason ...string) Ruling {
	return Ruling{Risk: Safe, Reason: first(reason)}
}

func Confirm(kind Kind, reason string) Ruling {
	return Ruling{Risk: NeedsConfirm, Kind: kind, Kinds: []Kind{kind}, Reason: reason}
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

type Decision struct {
	Action Action `json:"action"`
	Ruling Ruling `json:"ruling"`
	Reason string `json:"reason,omitempty"`
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
		remembered := len(ruling.ApprovalKinds()) != 0
		for _, kind := range ruling.ApprovalKinds() {
			remembered = remembered && memory != nil && memory.Contains(kind)
		}
		if remembered {
			return Decision{Action: ActionAllow, Ruling: ruling}
		}
		if config.Mode == Silent {
			return Decision{Action: ActionAllow, Ruling: ruling}
		}
	}
	return Decision{Action: ActionAsk, Ruling: ruling, Reason: ruling.Reason}
}

type Memory struct {
	mu    sync.RWMutex
	kinds map[Kind]struct{}
}

func NewMemory() *Memory {
	return &Memory{kinds: make(map[Kind]struct{})}
}

func (m *Memory) Add(kind Kind) {
	if m == nil || kind == KindNone || kind == KindDanger || kind == KindUnknown {
		return
	}
	m.mu.Lock()
	m.kinds[kind] = struct{}{}
	m.mu.Unlock()
}

func (m *Memory) Contains(kind Kind) bool {
	if m == nil || kind == KindNone || kind == KindDanger || kind == KindUnknown {
		return false
	}
	m.mu.RLock()
	_, ok := m.kinds[kind]
	m.mu.RUnlock()
	return ok
}
