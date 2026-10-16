package guard

import (
	"path"
	"strings"
	"time"
)

// GrantRulesSettingKey persists path-level grant rules; it shares the settings storage with device-level grants, so no migration is needed.
const GrantRulesSettingKey = "ai.grant_rules"

// Resource describes the action and primary path of a call awaiting authorization, for path-level rule matching.
type Resource struct {
	Action string `json:"action"`
	Path   string `json:"path,omitempty"`
}

// GrantRule is a persistent fine-grained allow rule: a limited scope for an action on a device.
// An empty Path means the action is not path-limited (whole-device for that action); ExpiresAt 0 means permanent.
type GrantRule struct {
	ID        string `json:"id"`
	DeviceID  string `json:"deviceId"`
	Action    string `json:"action"`
	Path      string `json:"path,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

var ruleActions = map[string]struct{}{
	"write_file":    {},
	"edit_file":     {},
	"exec_commands": {},
	"send_keys":     {},
}

// RuleActionSupported reports whether the action supports path-level grant rules.
func RuleActionSupported(action string) bool {
	_, ok := ruleActions[action]
	return ok
}

// RuleActionNeedsPath reports whether the action's rules must be limited to a path scope.
func RuleActionNeedsPath(action string) bool {
	return action == "write_file" || action == "edit_file"
}

// DirPattern derives the grant scope from a file path: the containing directory and all its
// sub-content (/var/log/app.log -> /var/log/**). Root-level and directory-less paths degrade to
// exact matching, avoiding accidental widening to the whole disk.
func DirPattern(path string) string {
	path = strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	if path == "" {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index <= 0 || index == 2 && path[1] == ':' {
		return path
	}
	return path[:index] + "/**"
}

// NormalizePath returns the normalized path shared by rule matching and execution: trims surrounding
// whitespace, converts backslashes to slashes, and applies path.Clean. The second return value false
// means the normalized path still contains a .. segment (out of bounds); the caller must reject it outright.
func NormalizePath(p string) (string, bool) {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return "", true
	}
	volume := ""
	if len(p) >= 3 && p[1] == ':' && p[2] == '/' {
		volume, p = p[:2], p[2:]
	}
	cleaned := path.Clean(p)
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", false
		}
	}
	return volume + cleaned, true
}

// MatchPath matches a path pattern segment by segment: whole segments match exactly, * and ? are
// supported within a segment, and ** spans any number of segments (including zero). Pattern and path
// are both normalized via NormalizePath first; matching uses the same normalized path as execution.
// Out-of-bounds paths still containing .. after normalization match no rule; ** never swallows ...
// An empty pattern matches any path (action-level grant).
func MatchPath(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	normalizedPattern, patternOK := NormalizePath(pattern)
	normalizedValue, valueOK := NormalizePath(value)
	if !patternOK || !valueOK {
		return false
	}
	return matchSegments(strings.Split(normalizedPattern, "/"), strings.Split(normalizedValue, "/"))
}

func matchSegments(pattern, value []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			if len(pattern) == 1 {
				return true
			}
			for skip := 0; skip <= len(value); skip++ {
				if matchSegments(pattern[1:], value[skip:]) {
					return true
				}
			}
			return false
		}
		if len(value) == 0 {
			return false
		}
		if !matchSegment(pattern[0], value[0]) {
			return false
		}
		pattern = pattern[1:]
		value = value[1:]
	}
	return len(value) == 0
}

func matchSegment(pattern, value string) bool {
	for len(pattern) > 0 {
		if pattern[0] == '*' {
			for skip := 0; skip <= len(value); skip++ {
				if matchSegment(pattern[1:], value[skip:]) {
					return true
				}
			}
			return false
		}
		if len(value) == 0 {
			return false
		}
		if pattern[0] != '?' && pattern[0] != value[0] {
			return false
		}
		pattern = pattern[1:]
		value = value[1:]
	}
	return len(value) == 0
}

// Expired reports whether the rule has expired at the given time; ExpiresAt 0 means it never expires.
func (r GrantRule) Expired(now time.Time) bool {
	return r.ExpiresAt != 0 && now.UnixMilli() >= r.ExpiresAt
}

// Covers reports whether the rule covers the given action and path (ignoring device ownership and expiry).
func (r GrantRule) Covers(action, path string) bool {
	return r.Action == action && MatchPath(r.Path, path)
}
