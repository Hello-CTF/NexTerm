package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// PreferenceStore is the minimal read/write subset of the setting table, injected by the wiring layer as *store.Store.
type PreferenceStore interface {
	SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) error
	SettingListPrefix(ctx context.Context, prefix string) (map[string]string, error)
}

type preferenceTombstoneRecorder interface {
	SyncTombstoneRecord(ctx context.Context, id, kind string, deletedAt int64) error
	PreferencesSyncOptIn(ctx context.Context, userID string) (bool, error)
}

// Preferences are stored in the setting table as namespaced keys: global defaults are writable only by the super admin, user overrides only by their owner.
const (
	preferenceDefaultPrefix = "prefs.default."
	preferenceUserPrefix    = "prefs.user."
)

func preferenceUserKeyPrefix(userID string) string {
	return preferenceUserPrefix + userID + "."
}

func PreferenceUserKeyPrefix(userID string) string {
	return preferenceUserKeyPrefix(userID)
}

func PreferenceObjectID(key string) string {
	return "preference:" + key
}

func NormalizePreferenceValue(key string, raw json.RawMessage) (string, error) {
	return validatePreferenceValue(key, raw)
}

func PreferenceKeyDeclared(key string) bool {
	_, ok := preferenceSpecs[key]
	return ok
}

type preferenceKind int

const (
	preferenceKindEnumFloat preferenceKind = iota
	preferenceKindHalfRange
	preferenceKindIntRange
	preferenceKindEnumString
	preferenceKindBool
	preferenceKindBinding
	preferenceKindKeyBar
	preferenceKindQuickCommands
)

type preferenceSpec struct {
	kind    preferenceKind
	floats  []float64
	min     int64
	max     int64
	strings []string
}

// preferenceSpecs is the whitelist of all readable/writable preference keys; DEK, password, credential, and arbitrary keys are never included.
// keybinding.focusUpperPane, focusLowerPane and toggleSplit were replaced by the split tree actions; they stay declared so
// clients can read values saved before and clear them after moving them to the new keys. keybinding.toggleSessionPanel
// belongs to the removed session side panel; it stays declared so older clients and synced tombstones keep working.
var preferenceSpecs = map[string]preferenceSpec{
	"appearance.uiFontPreset":           {kind: preferenceKindHalfRange, min: 8, max: 32},
	"appearance.uiFontScale":            {kind: preferenceKindEnumFloat, floats: []float64{1, 1.25, 1.5, 1.75, 2}},
	"appearance.terminalFontSize":       {kind: preferenceKindIntRange, min: 8, max: 32},
	"appearance.mobileTerminalFontSize": {kind: preferenceKindIntRange, min: 8, max: 32},
	"appearance.terminalFontFamily":     {kind: preferenceKindEnumString, strings: []string{"cascadia", "system", "jetbrains-mono", "fira-code", "source-code-pro", "menlo", "consolas"}},
	"appearance.terminalTheme":          {kind: preferenceKindEnumString, strings: []string{"dark", "light", "interface"}},
	"appearance.terminalContrast":       {kind: preferenceKindEnumString, strings: []string{"standard", "high", "maximum"}},
	"appearance.themeMode":              {kind: preferenceKindEnumString, strings: []string{"system", "light", "dark"}},
	"input.selectionAutoCopy":           {kind: preferenceKindBool},
	"terminal.closeAction":              {kind: preferenceKindEnumString, strings: []string{"ask", "kill", "detach"}},
	"terminal.defaultMode":              {kind: preferenceKindEnumString, strings: []string{"durable", "regular"}},
	"terminal.gpuAcceleration":          {kind: preferenceKindEnumString, strings: []string{"auto", "on", "off"}},
	"terminal.linkActivation":           {kind: preferenceKindEnumString, strings: []string{"modifier", "click"}},
	"terminal.confirmMultilinePaste":    {kind: preferenceKindEnumString, strings: []string{"unbracketed", "always", "never"}},
	"terminal.durableHistoryMiB":        {kind: preferenceKindIntRange, min: 1, max: 1024},
	"terminal.keyBar":                   {kind: preferenceKindKeyBar},
	"terminal.quickCommands":            {kind: preferenceKindQuickCommands},
	"keybinding.commandPalette":         {kind: preferenceKindBinding},
	"keybinding.quickConnect":           {kind: preferenceKindBinding},
	"keybinding.newTerminal":            {kind: preferenceKindBinding},
	"keybinding.nextWorkspace":          {kind: preferenceKindBinding},
	"keybinding.previousWorkspace":      {kind: preferenceKindBinding},
	"keybinding.nextTab":                {kind: preferenceKindBinding},
	"keybinding.previousTab":            {kind: preferenceKindBinding},
	"keybinding.focusUpperPane":         {kind: preferenceKindBinding},
	"keybinding.focusLowerPane":         {kind: preferenceKindBinding},
	"keybinding.focusPaneLeft":          {kind: preferenceKindBinding},
	"keybinding.focusPaneRight":         {kind: preferenceKindBinding},
	"keybinding.focusPaneUp":            {kind: preferenceKindBinding},
	"keybinding.focusPaneDown":          {kind: preferenceKindBinding},
	"keybinding.toggleSidebar":          {kind: preferenceKindBinding},
	"keybinding.toggleAiSidebar":        {kind: preferenceKindBinding},
	"keybinding.toggleSessionPanel":     {kind: preferenceKindBinding},
	"keybinding.closeTab":               {kind: preferenceKindBinding},
	"keybinding.toggleSplit":            {kind: preferenceKindBinding},
	"keybinding.splitRight":             {kind: preferenceKindBinding},
	"keybinding.splitDown":              {kind: preferenceKindBinding},
	"keybinding.closePane":              {kind: preferenceKindBinding},
	"keybinding.switchTab":              {kind: preferenceKindBinding},
	"keybinding.restoreClosedTab":       {kind: preferenceKindBinding},
	"keybinding.terminalSearch":         {kind: preferenceKindBinding},
	"keybinding.reclaimTakeover":        {kind: preferenceKindBinding},
	"keybinding.lockVault":              {kind: preferenceKindBinding},
	"keybinding.openSettings":           {kind: preferenceKindBinding},
	"keybinding.focusAiInput":           {kind: preferenceKindBinding},
}

// PreferenceKeys returns a copy of the whitelist keys, for the HTTP layer and tests to enumerate declared preferences.
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

// normalizePreferenceBinding validates a key binding string: modifiers are deduplicated, at most one of Mod/Primary/Ctrl/Meta,
// the bare key is limited to named keys and F keys, and the output uses the canonical order (Mod,Primary,Ctrl,Meta,Shift,Alt+key).
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

// validatePreferenceValue validates and normalizes a single value, returning JSON text ready for storage.
func validatePreferenceValue(key string, raw json.RawMessage) (string, error) {
	spec, ok := preferenceSpecs[key]
	if !ok {
		return "", ipc.BadParam(fmt.Errorf("未声明的偏好键 %q", key))
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
	case preferenceKindHalfRange:
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil || value < float64(spec.min) || value > float64(spec.max) || value*2 != float64(int64(value*2)) {
			return "", invalidPreferenceValue(key)
		}
		return strconv.FormatFloat(value, 'f', -1, 64), nil
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
	case preferenceKindKeyBar:
		if canonical, ok := normalizeKeyBar(raw); ok {
			return canonical, nil
		}
	case preferenceKindQuickCommands:
		if canonical, ok := normalizeQuickCommands(raw); ok {
			return canonical, nil
		}
	}
	return "", invalidPreferenceValue(key)
}

// Limits of the terminal key bar and quick command preferences. The quick command list as a whole stays well
// below the 64 KiB account request body, so a full list can always be saved in one request.
const (
	maxKeyBarKeys            = 64
	maxQuickCommands         = 50
	maxQuickCommandLabel     = 32
	maxQuickCommandTextBytes = 4 << 10
	maxQuickCommandsBytes    = 48 << 10
)

var keyBarIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// normalizeKeyBar validates the ordered key ids of the terminal key bar. Ids are not checked against the client
// catalog, so a layout saved by a newer client survives on an older one, which skips ids it does not know.
func normalizeKeyBar(raw json.RawMessage) (string, bool) {
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil || ids == nil || len(ids) > maxKeyBarKeys {
		return "", false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !keyBarIDPattern.MatchString(id) || seen[id] {
			return "", false
		}
		seen[id] = true
	}
	out, err := json.Marshal(ids)
	if err != nil {
		return "", false
	}
	return string(out), true
}

type quickCommand struct {
	Label       string `json:"label"`
	Text        string `json:"text"`
	AppendEnter bool   `json:"appendEnter"`
}

// normalizeQuickCommands validates user-defined terminal quick commands: a label of at most 32 characters without
// control characters, a non-empty text of at most 4 KiB without NUL (newlines and escape sequences are allowed),
// and whether Enter follows the text.
func normalizeQuickCommands(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var commands []quickCommand
	if err := decoder.Decode(&commands); err != nil || decoder.More() || commands == nil || len(commands) > maxQuickCommands {
		return "", false
	}
	for _, command := range commands {
		if !validQuickCommandLabel(command.Label) || !validQuickCommandText(command.Text) {
			return "", false
		}
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(commands); err != nil {
		return "", false
	}
	canonical := strings.TrimSuffix(out.String(), "\n")
	if len(canonical) > maxQuickCommandsBytes {
		return "", false
	}
	return canonical, true
}

func validQuickCommandLabel(label string) bool {
	if strings.TrimSpace(label) == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > maxQuickCommandLabel {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validQuickCommandText(text string) bool {
	return text != "" && len(text) <= maxQuickCommandTextBytes && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func invalidPreferenceValue(key string) error {
	return ipc.BadParam(fmt.Errorf("偏好 %q 的值不合法", key))
}

// preferenceUpdate translates set/clear into one atomic write of namespaced keys.
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
			return nil, nil, ipc.BadParam(fmt.Errorf("未声明的偏好键 %q", key))
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

// preferenceList lists the key/value pairs set under a namespace; values are the stored JSON text.
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
	updated, err := p.preferenceUpdate(ctx, preferenceUserKeyPrefix(userID), set, clear)
	if err != nil {
		return nil, err
	}
	recorder, ok := p.store.(preferenceTombstoneRecorder)
	if !ok {
		return updated, nil
	}
	enabled, err := recorder.PreferencesSyncOptIn(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return updated, nil
	}
	for _, key := range clear {
		if err := recorder.SyncTombstoneRecord(ctx, PreferenceObjectID(key), "preference", ids.NowMS()); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// MergePreferences merges effective values with "override wins over default"; an explicit null (unbind) is also a valid override.
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
