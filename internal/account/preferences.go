package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// PreferenceStore 是 setting 表的最小读写子集, 由装配层以 *store.Store 注入。
type PreferenceStore interface {
	SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) error
	SettingListPrefix(ctx context.Context, prefix string) (map[string]string, error)
}

// 偏好以命名空间键存入 setting 表: 全局默认只有超管可写, 用户覆盖只能由本人读写。
const (
	preferenceDefaultPrefix = "prefs.default."
	preferenceUserPrefix    = "prefs.user."
)

func preferenceUserKeyPrefix(userID string) string {
	return preferenceUserPrefix + userID + "."
}

type preferenceKind int

const (
	preferenceKindEnumFloat preferenceKind = iota
	preferenceKindIntRange
	preferenceKindEnumString
	preferenceKindBool
	preferenceKindBinding
)

type preferenceSpec struct {
	kind    preferenceKind
	floats  []float64
	min     int64
	max     int64
	strings []string
}

// preferenceSpecs 是允许读写的全部偏好键白名单; DEK、密码、凭据与任意键一律不在其中。
var preferenceSpecs = map[string]preferenceSpec{
	"appearance.uiFontPreset":     {kind: preferenceKindEnumFloat, floats: []float64{12, 13, 14.5}},
	"appearance.uiFontScale":      {kind: preferenceKindEnumFloat, floats: []float64{1, 1.25, 1.5, 1.75, 2}},
	"appearance.terminalFontSize": {kind: preferenceKindIntRange, min: 12, max: 18},
	"appearance.terminalTheme":    {kind: preferenceKindEnumString, strings: []string{"dark", "light", "interface"}},
	"input.selectionAutoCopy":     {kind: preferenceKindBool},
	"keybinding.commandPalette":   {kind: preferenceKindBinding},
	"keybinding.quickConnect":     {kind: preferenceKindBinding},
	"keybinding.newTerminal":      {kind: preferenceKindBinding},
	"keybinding.toggleSidebar":    {kind: preferenceKindBinding},
	"keybinding.toggleAiSidebar":  {kind: preferenceKindBinding},
	"keybinding.closeTab":         {kind: preferenceKindBinding},
	"keybinding.toggleSplit":      {kind: preferenceKindBinding},
	"keybinding.switchTab":        {kind: preferenceKindBinding},
	"keybinding.terminalSearch":   {kind: preferenceKindBinding},
	"keybinding.reclaimTakeover":  {kind: preferenceKindBinding},
	"keybinding.syncNow":          {kind: preferenceKindBinding},
}

// PreferenceKeys 返回白名单键的副本, 供 HTTP 层与测试枚举声明的偏好。
func PreferenceKeys() []string {
	keys := make([]string, 0, len(preferenceSpecs))
	for key := range preferenceSpecs {
		keys = append(keys, key)
	}
	return keys
}

var preferenceNamedKeys = map[string]bool{
	"Enter": true, "Escape": true, "Tab": true, "Space": true, "Backspace": true,
	"Delete": true, "Home": true, "End": true, "PageUp": true, "PageDown": true,
	"ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true,
}

var preferenceBindingModifiers = map[string]bool{
	"Mod": true, "Primary": true, "Ctrl": true, "Meta": true, "Shift": true, "Alt": true,
}

// normalizePreferenceBinding 校验键位绑定串: 修饰键去重、Mod/Primary/Ctrl/Meta 至多一个、
// 裸键仅限命名键与 F 键, 输出规范化顺序(Mod,Primary,Ctrl,Meta,Shift,Alt+key)。
func normalizePreferenceBinding(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	parts := strings.Split(input, "+")
	key, ok := normalizePreferenceKey(parts[len(parts)-1])
	if !ok {
		return "", false
	}
	seen := map[string]bool{}
	var order []string
	for _, part := range parts[:len(parts)-1] {
		if !preferenceBindingModifiers[part] || seen[part] {
			return "", false
		}
		seen[part] = true
		order = append(order, part)
	}
	modLikes := 0
	for _, part := range []string{"Mod", "Primary", "Ctrl", "Meta"} {
		if seen[part] {
			modLikes++
		}
	}
	if modLikes > 1 {
		return "", false
	}
	if modLikes == 0 && !seen["Shift"] && !seen["Alt"] && !preferenceNamedKeys[key] && !isPreferenceFKey(key) {
		return "", false
	}
	canonical := make([]string, 0, len(order)+1)
	for _, part := range []string{"Mod", "Primary", "Ctrl", "Meta", "Shift", "Alt"} {
		if seen[part] {
			canonical = append(canonical, part)
		}
	}
	return strings.Join(append(canonical, key), "+"), true
}

func normalizePreferenceKey(raw string) (string, bool) {
	if raw == "1-9" || preferenceNamedKeys[raw] || isPreferenceFKey(raw) {
		return raw, true
	}
	if len(raw) == 1 && raw[0] >= 0x21 && raw[0] <= 0x7e && raw != "+" {
		return strings.ToLower(raw), true
	}
	return "", false
}

func isPreferenceFKey(raw string) bool {
	if len(raw) < 2 || len(raw) > 3 || raw[0] != 'F' {
		return false
	}
	number, err := strconv.Atoi(raw[1:])
	if err != nil || number < 1 || number > 24 {
		return false
	}
	return len(raw) == 2 || raw[1] != '0'
}

// validatePreferenceValue 校验并规范化单个值, 返回可入库的 JSON 文本。
func validatePreferenceValue(key string, raw json.RawMessage) (string, error) {
	spec, ok := preferenceSpecs[key]
	if !ok {
		return "", ipc.BadParam(fmt.Errorf("参数错误: 未声明的偏好键 %q", key))
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if spec.kind == preferenceKindBinding {
			return "null", nil
		}
		return "", invalidPreferenceValue(key)
	}
	switch spec.kind {
	case preferenceKindEnumFloat:
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", invalidPreferenceValue(key)
		}
		for _, allowed := range spec.floats {
			if value == allowed {
				return strconv.FormatFloat(value, 'f', -1, 64), nil
			}
		}
	case preferenceKindIntRange:
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil || value < spec.min || value > spec.max {
			return "", invalidPreferenceValue(key)
		}
		return strconv.FormatInt(value, 10), nil
	case preferenceKindEnumString:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", invalidPreferenceValue(key)
		}
		for _, allowed := range spec.strings {
			if value == allowed {
				return strconv.Quote(value), nil
			}
		}
	case preferenceKindBool:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", invalidPreferenceValue(key)
		}
		return strconv.FormatBool(value), nil
	case preferenceKindBinding:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", invalidPreferenceValue(key)
		}
		normalized, ok := normalizePreferenceBinding(value)
		if !ok {
			return "", invalidPreferenceValue(key)
		}
		return strconv.Quote(normalized), nil
	}
	return "", invalidPreferenceValue(key)
}

func invalidPreferenceValue(key string) error {
	return ipc.BadParam(fmt.Errorf("参数错误: 偏好 %q 的值不合法", key))
}

// preferenceUpdate 把 set/clear 翻译成一次原子写入的命名空间键。
func preferenceUpdate(prefix string, set map[string]json.RawMessage, clear []string) (map[string]string, []string, error) {
	values := map[string]string{}
	for key, raw := range set {
		canonical, err := validatePreferenceValue(key, raw)
		if err != nil {
			return nil, nil, err
		}
		values[prefix+key] = canonical
	}
	deleteKeys := make([]string, 0, len(clear))
	for _, key := range clear {
		if _, ok := preferenceSpecs[key]; !ok {
			return nil, nil, ipc.BadParam(fmt.Errorf("参数错误: 未声明的偏好键 %q", key))
		}
		deleteKeys = append(deleteKeys, prefix+key)
	}
	return values, deleteKeys, nil
}

type Preferences struct {
	store PreferenceStore
}

func NewPreferences(store PreferenceStore) *Preferences {
	return &Preferences{store: store}
}

// preferenceList 列出命名空间下已设置的键值, 值为入库的 JSON 文本。
func (p *Preferences) preferenceList(ctx context.Context, prefix string) (map[string]json.RawMessage, error) {
	stored, err := p.store.SettingListPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	values := make(map[string]json.RawMessage, len(stored))
	for key, value := range stored {
		values[strings.TrimPrefix(key, prefix)] = json.RawMessage(value)
	}
	return values, nil
}

func (p *Preferences) preferenceUpdate(ctx context.Context, prefix string, set map[string]json.RawMessage, clear []string) (map[string]json.RawMessage, error) {
	values, deleteKeys, err := preferenceUpdate(prefix, set, clear)
	if err != nil {
		return nil, err
	}
	if err := p.store.SettingSetManyDelete(ctx, values, deleteKeys...); err != nil {
		return nil, err
	}
	return p.preferenceList(ctx, prefix)
}

func (p *Preferences) Defaults(ctx context.Context) (map[string]json.RawMessage, error) {
	return p.preferenceList(ctx, preferenceDefaultPrefix)
}

func (p *Preferences) UpdateDefaults(ctx context.Context, set map[string]json.RawMessage, clear []string) (map[string]json.RawMessage, error) {
	return p.preferenceUpdate(ctx, preferenceDefaultPrefix, set, clear)
}

func (p *Preferences) UserOverrides(ctx context.Context, userID string) (map[string]json.RawMessage, error) {
	return p.preferenceList(ctx, preferenceUserKeyPrefix(userID))
}

func (p *Preferences) UpdateUserOverrides(ctx context.Context, userID string, set map[string]json.RawMessage, clear []string) (map[string]json.RawMessage, error) {
	return p.preferenceUpdate(ctx, preferenceUserKeyPrefix(userID), set, clear)
}

// MergePreferences 以「覆盖优先于默认」合成生效值; 显式 null(解绑)也是有效覆盖。
func MergePreferences(defaults, overrides map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(defaults)+len(overrides))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range overrides {
		merged[key] = value
	}
	return merged
}
