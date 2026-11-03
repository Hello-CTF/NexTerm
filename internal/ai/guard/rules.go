package guard

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// GrantRulesSettingKey persists path-level grant rules; it shares the settings storage with device-level grants, so no migration is needed.
const GrantRulesSettingKey = "ai.grant_rules"

// Sentinel validation errors for grant-rule mutations; IPC layers classify
// them with errors.Is instead of matching error text.
var (
	ErrRuleDeviceRequired    = errors.New("授权规则缺少设备 ID")
	ErrRuleActionUnsupported = errors.New("授权规则动作仅支持 write_file/edit_file/exec_commands/send_keys")
	ErrRulePathRequired      = errors.New("文件写入规则必须限定路径范围")
	ErrRulePathUnsupported   = errors.New("该动作的规则不支持路径范围")
	ErrRuleExpiryInvalid     = errors.New("授权规则过期时间必须晚于当前时间")
	ErrRuleIDRequired        = errors.New("授权规则缺少规则 ID")
	ErrRuleScopeRequired     = errors.New("命令类授权规则必须限定操作种类，不能整机放行")
	ErrRuleScopeUnsupported  = errors.New("路径类授权规则不支持操作种类范围")
)

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
	// Environment is the resolved environment name of the target asset.
	Environment string `json:"environment,omitempty"`
	// Production marks the target as production under the owner's configured
	// production environment list; callers that resolve custom names set it
	// explicitly, while built-in names are always recognized.
	Production bool `json:"production,omitempty"`
}

// ProductionTarget reports whether the resource targets a production environment,
// either explicitly resolved by the caller or via the built-in production names.
func (r Resource) ProductionTarget() bool {
	return r.Production || IsBuiltInProductionEnvironment(r.Environment)
}

// GrantRule is a persistent fine-grained allow rule: a limited scope for an action on a device.
// An empty Path means the action is not path-limited (whole-device for that action); ExpiresAt 0 means permanent.
type GrantRule struct {
	ID       string `json:"id"`
	DeviceID string `json:"deviceId"`
	Action   string `json:"action"`
	Path     string `json:"path,omitempty"`
	// Kinds and Programs bound a command rule to the approval kinds and
	// programs it covers; Unattended lets scheduled runs use the rule.
	Kinds      []Kind   `json:"kinds,omitempty"`
	Programs   []string `json:"programs,omitempty"`
	Unattended bool     `json:"unattended,omitempty"`
	ExpiresAt  int64    `json:"expiresAt,omitempty"`
	CreatedAt  int64    `json:"createdAt"`
	UpdatedAt  int64    `json:"updatedAt"`
	generation string
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
	if runtime.GOOS != "windows" {
		if cleaned, ok, handled := normalizeWindowsStyle(p); handled {
			return cleaned, ok
		}
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

// normalizeWindowsStyle normalizes drive-letter and UNC paths with Windows semantics (both
// backslash and slash separate segments) regardless of the host OS, since the target device may be
// Windows while the host is not. handled is false for paths that are not Windows-style.
func normalizeWindowsStyle(p string) (cleaned string, ok, handled bool) {
	var volume, rest string
	switch {
	case len(p) >= 2 && p[1] == ':' && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z'):
		volume, rest = p[:2], p[2:]
	case strings.HasPrefix(p, `\\`):
		parts := strings.SplitN(strings.ReplaceAll(p[2:], `\`, "/"), "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", false, true
		}
		volume = "//" + parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			rest = "/" + parts[2]
		}
	default:
		return "", false, false
	}
	rest = strings.ReplaceAll(rest, `\`, "/")
	if rest == "" {
		return volume, true, true
	}
	rooted := strings.HasPrefix(rest, "/")
	rest = path.Clean(rest)
	if !rooted && rest == "." {
		return volume, true, true
	}
	for _, segment := range strings.Split(rest, "/") {
		if segment == ".." {
			return "", false, true
		}
	}
	return volume + rest, true, true
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

// RuleScope bounds what a command rule covers beyond its action and path.
type RuleScope struct {
	Kinds      []Kind
	Programs   []string
	Unattended bool
}

// normalized returns the scope with sorted, de-duplicated kinds and programs.
func (s RuleScope) normalized() RuleScope {
	kinds := append([]Kind(nil), s.Kinds...)
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	s.Kinds = slices.Compact(kinds)
	programs := append([]string(nil), s.Programs...)
	sort.Strings(programs)
	s.Programs = slices.Compact(programs)
	if len(s.Kinds) == 0 {
		s.Kinds = nil
	}
	if len(s.Programs) == 0 {
		s.Programs = nil
	}
	return s
}

// ValidateRuleScope checks the action, path and scope combination of a rule.
func ValidateRuleScope(action, path string, scope RuleScope) error {
	if !RuleActionSupported(action) {
		return ErrRuleActionUnsupported
	}
	path = strings.TrimSpace(path)
	if RuleActionNeedsPath(action) {
		if path == "" {
			return ErrRulePathRequired
		}
		if len(scope.Kinds) != 0 || len(scope.Programs) != 0 {
			return ErrRuleScopeUnsupported
		}
		return nil
	}
	if path != "" {
		return ErrRulePathUnsupported
	}
	if !Rememberable(Ruling{Risk: NeedsConfirm, Kinds: scope.Kinds, Programs: scope.Programs}) {
		return ErrRuleScopeRequired
	}
	if len(scope.Programs) != 0 && !slices.Contains(scope.Kinds, KindProgram) {
		return ErrRuleScopeRequired
	}
	return nil
}

// Legacy reports an old whole-device command rule: it is kept and listed but
// never authorizes anything automatically.
func (r GrantRule) Legacy() bool {
	return !RuleActionNeedsPath(r.Action) && len(r.Kinds) == 0
}

// Covers reports whether the rule covers the given action, path and ruling
// (ignoring device ownership and expiry).
func (r GrantRule) Covers(action, path string, ruling Ruling) bool {
	if r.Action != action || !MatchPath(r.Path, path) || !Rememberable(ruling) {
		return false
	}
	if RuleActionNeedsPath(r.Action) {
		return true
	}
	if r.Legacy() {
		return false
	}
	for _, kind := range ruling.ApprovalKinds() {
		if !slices.Contains(r.Kinds, kind) {
			return false
		}
	}
	for _, program := range ruling.Programs {
		if !slices.Contains(r.Programs, program) {
			return false
		}
	}
	return true
}

// PermanentScope describes what a permanent grant for the confirmed call would
// cover; empty means a permanent grant is not offered.
func PermanentScope(ruling Ruling, resource Resource) string {
	if ruling.Risk != NeedsConfirm || !RuleActionSupported(resource.Action) {
		return ""
	}
	if RuleActionNeedsPath(resource.Action) {
		pattern := DirPattern(resource.Path)
		if pattern == "" {
			return ""
		}
		verb := "写入"
		if resource.Action == "edit_file" {
			verb = "修改"
		}
		if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
			return verb + " " + dir + " 下的文件"
		}
		return verb + " " + pattern
	}
	scope := SessionScope(ruling)
	if scope == "" {
		return ""
	}
	return "该设备上的" + scope
}
