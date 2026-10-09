package guard

import (
	"path"
	"strings"
	"time"
)

// GrantRulesSettingKey 持久化路径级授权规则；与设备级授权同处设置存储，无需迁移。
const GrantRulesSettingKey = "ai.grant_rules"

// Resource 描述一次待授权调用的动作与主路径，供路径级规则匹配。
type Resource struct {
	Action string `json:"action"`
	Path   string `json:"path,omitempty"`
}

// GrantRule 是一条持久的细粒度允许规则：某设备上某动作的限定范围。
// Path 为空表示该动作不限路径（整设备该动作）；ExpiresAt 为 0 表示永久。
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

// RuleActionSupported 报告动作是否支持路径级授权规则。
func RuleActionSupported(action string) bool {
	_, ok := ruleActions[action]
	return ok
}

// RuleActionNeedsPath 报告该动作的规则是否必须限定路径范围。
func RuleActionNeedsPath(action string) bool {
	return action == "write_file" || action == "edit_file"
}

// DirPattern 从文件路径推导授权范围：所在目录及其全部子内容（/var/log/app.log → /var/log/**）。
// 根目录与无目录路径退化为精确匹配，避免意外放大到整盘。
func DirPattern(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index <= 0 {
		return path
	}
	return path[:index] + "/**"
}

// NormalizePath 返回规则匹配与执行共用的规范化路径：去首尾空白并 path.Clean。
// 第二个返回值为 false 表示规范化后路径仍含 .. 段（越界），调用方必须直接拒绝。
func NormalizePath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", true
	}
	cleaned := path.Clean(p)
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return "", false
		}
	}
	return cleaned, true
}

// MatchPath 按段匹配路径模式：整段相等匹配，段内支持 * 与 ?，** 跨任意段（含零段）。
// 模式与路径都先经 NormalizePath 规范化，匹配使用与执行相同的规范化路径；
// 规范化后仍含 .. 的越界路径不匹配任何规则，** 不会吞掉 ..。
// 空模式匹配任意路径（动作级授权）。
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

// Expired 报告规则在给定时刻是否已过期；ExpiresAt 为 0 表示永久不过期。
func (r GrantRule) Expired(now time.Time) bool {
	return r.ExpiresAt != 0 && now.UnixMilli() >= r.ExpiresAt
}

// Covers 报告规则是否覆盖给定动作与路径（不考虑设备归属与过期）。
func (r GrantRule) Covers(action, path string) bool {
	return r.Action == action && MatchPath(r.Path, path)
}
