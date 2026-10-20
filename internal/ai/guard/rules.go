package guard

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// GrantRulesSettingKey persists path-level grant rules; it shares the settings storage with device-level grants, so no migration is needed.
const GrantRulesSettingKey = "ai.grant_rules"

func GrantRulesSettingKeyForUser(ctx context.Context) string {
	if userID, ok := ipc.UserIDFromContext(ctx); ok {
		return GrantRulesSettingKey + ".user." + userID
	}
	return GrantRulesSettingKey
}

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
func DirPattern(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	dir := filepath.Dir(name)
	if dir == "." || dir == filepath.Dir(dir) {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(dir) + "/**"
}

// NormalizePath returns the platform-native cleaned path used by execution. The second return value
// false means the cleaned path still contains a .. segment outside its volume root; the caller must
// reject it outright.
func NormalizePath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", true
	}
	cleaned := filepath.Clean(p)
	volume := filepath.VolumeName(cleaned)
	for _, segment := range strings.Split(strings.TrimPrefix(cleaned, volume), string(filepath.Separator)) {
		if segment == ".." {
			return "", false
		}
	}
	return cleaned, true
}

// MatchPath matches a path pattern segment by segment: whole segments match exactly, * and ? are
// supported within a segment, and ** spans any number of segments (including zero). Pattern and path
// are both normalized via NormalizePath first, then matched in their slash-separated form.
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
	normalizedPattern = filepath.ToSlash(normalizedPattern)
	normalizedValue = filepath.ToSlash(normalizedValue)
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
