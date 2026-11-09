// Package highlight validates the terminal keyword highlight settings stored
// in the account preference terminal.keywordHighlights. The frontend renders
// the highlights; this package only bounds what may be stored and synced.
//
// Patterns run in the browser's backtracking regular expression engine, so
// besides the RE2 program size caps shared with state rules, a regular
// expression may not repeat without bound a sub-expression that itself
// repeats without bound or alternates (the shape of catastrophic
// backtracking, such as (a+)+ or (ab|a)*). The frontend applies the same
// check before saving.
package highlight

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
)

// Limits of the keyword highlight settings. The whole value stays below the
// 64 KiB account request body.
const (
	MaxRules        = 50
	MaxPatternBytes = 256
	MaxAssets       = 32
	MaxPresets      = 32
	MaxValueBytes   = 48 << 10
)

// Colors is the palette a rule may use; the frontend maps each name to a
// color that reads on light and dark terminal themes.
var Colors = []string{"red", "orange", "yellow", "green", "cyan", "blue", "magenta", "gray"}

var (
	ruleIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	presetIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// Rule is one user-defined highlight rule. Rules are applied in order; an
// earlier rule wins where matches overlap.
type Rule struct {
	ID            string `json:"id"`
	Pattern       string `json:"pattern"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`
	WholeWord     bool   `json:"wholeWord"`
	// Foreground and Background are palette names; empty keeps the
	// terminal's own color.
	Foreground string `json:"foreground"`
	Background string `json:"background"`
	Underline  bool   `json:"underline"`
	Enabled    bool   `json:"enabled"`
	// AssetIDs limits the rule to sessions of these assets; empty applies
	// it to every session.
	AssetIDs []string `json:"assetIds,omitempty"`
}

// Settings is the stored value: the enabled built-in presets and the user's
// rules. Preset IDs are not checked against the client's catalog, so a
// preset added by a newer client survives on an older one, which skips it.
type Settings struct {
	Presets []string `json:"presets"`
	Rules   []Rule   `json:"rules"`
}

// Normalize validates a stored or submitted value and returns its canonical
// JSON text.
func Normalize(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var settings Settings
	if err := decoder.Decode(&settings); err != nil || decoder.More() {
		return "", errors.New("关键词高亮设置格式无效")
	}
	if settings.Presets == nil {
		settings.Presets = []string{}
	}
	if settings.Rules == nil {
		settings.Rules = []Rule{}
	}
	if len(settings.Presets) > MaxPresets {
		return "", fmt.Errorf("预设数量超出上限 %d", MaxPresets)
	}
	seenPresets := make(map[string]bool, len(settings.Presets))
	for _, preset := range settings.Presets {
		if !presetIDPattern.MatchString(preset) || seenPresets[preset] {
			return "", fmt.Errorf("预设 %q 无效", preset)
		}
		seenPresets[preset] = true
	}
	if len(settings.Rules) > MaxRules {
		return "", fmt.Errorf("规则数量超出上限 %d", MaxRules)
	}
	seenRules := make(map[string]bool, len(settings.Rules))
	total := 0
	for i := range settings.Rules {
		rule := &settings.Rules[i]
		if !ruleIDPattern.MatchString(rule.ID) || seenRules[rule.ID] {
			return "", fmt.Errorf("第 %d 条规则的 ID 无效", i+1)
		}
		seenRules[rule.ID] = true
		size, err := ProgramSize(*rule)
		if err != nil {
			return "", fmt.Errorf("第 %d 条规则: %w", i+1, err)
		}
		total += size
		if err := validateStyle(*rule); err != nil {
			return "", fmt.Errorf("第 %d 条规则: %w", i+1, err)
		}
		if len(rule.AssetIDs) > MaxAssets {
			return "", fmt.Errorf("第 %d 条规则: 主机数量超出上限 %d", i+1, MaxAssets)
		}
		seenAssets := make(map[string]bool, len(rule.AssetIDs))
		for _, id := range rule.AssetIDs {
			if !ids.Valid(id) || seenAssets[id] {
				return "", fmt.Errorf("第 %d 条规则: 主机 ID 无效", i+1)
			}
			seenAssets[id] = true
		}
		if len(rule.AssetIDs) == 0 {
			rule.AssetIDs = nil
		}
	}
	if total > staterule.MaxSetProgram {
		return "", fmt.Errorf("规则过于复杂: 编译后共 %d 条指令，超出上限 %d", total, staterule.MaxSetProgram)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(settings); err != nil {
		return "", err
	}
	canonical := strings.TrimSuffix(out.String(), "\n")
	if len(canonical) > MaxValueBytes {
		return "", fmt.Errorf("关键词高亮设置超出 %d KiB", MaxValueBytes>>10)
	}
	return canonical, nil
}

func validateStyle(rule Rule) error {
	if rule.Foreground == "" && rule.Background == "" && !rule.Underline {
		return errors.New("请至少选择一种样式")
	}
	for _, color := range []string{rule.Foreground, rule.Background} {
		if color != "" && !validColor(color) {
			return fmt.Errorf("颜色 %q 无效", color)
		}
	}
	return nil
}

func validColor(color string) bool {
	for _, allowed := range Colors {
		if color == allowed {
			return true
		}
	}
	return false
}

// EffectivePattern is the RE2 pattern a rule matches: plain text is quoted
// and a case-insensitive rule gets the (?i) flag. Whole-word matching is a
// boundary check around each match and does not change the pattern.
func EffectivePattern(rule Rule) string {
	pattern := rule.Pattern
	if !rule.Regex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !rule.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	return pattern
}

// ProgramSize validates a rule's pattern and returns its compiled RE2
// program size.
func ProgramSize(rule Rule) (int, error) {
	if strings.TrimSpace(rule.Pattern) == "" {
		return 0, errors.New("匹配模式不能为空")
	}
	if len(rule.Pattern) > MaxPatternBytes {
		return 0, fmt.Errorf("匹配模式超出 %d 字节", MaxPatternBytes)
	}
	if !utf8.ValidString(rule.Pattern) || strings.IndexFunc(rule.Pattern, unicode.IsControl) >= 0 {
		return 0, errors.New("匹配模式不能包含控制字符")
	}
	pattern := EffectivePattern(rule)
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return 0, fmt.Errorf("正则表达式无效: %v", err)
	}
	if nestedRepeat(parsed, false) {
		return 0, errors.New("正则表达式含有嵌套的无限重复，匹配可能极慢")
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return 0, fmt.Errorf("正则表达式无效: %v", err)
	}
	if len(program.Inst) > staterule.MaxProgram {
		return 0, fmt.Errorf("正则表达式过于复杂: 编译后 %d 条指令，超出上限 %d", len(program.Inst), staterule.MaxProgram)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, fmt.Errorf("正则表达式无效: %v", err)
	}
	if re.MatchString("") {
		return 0, errors.New("匹配模式不能匹配空文本")
	}
	return len(program.Inst), nil
}

func unbounded(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpStar, syntax.OpPlus:
		return true
	case syntax.OpRepeat:
		return re.Max < 0
	}
	return false
}

// nestedRepeat reports whether an unbounded repeat contains another unbounded
// repeat or an alternation. inside is set below an unbounded repeat.
func nestedRepeat(re *syntax.Regexp, inside bool) bool {
	if inside && (unbounded(re) || re.Op == syntax.OpAlternate) {
		return true
	}
	inside = inside || unbounded(re)
	for _, sub := range re.Sub {
		if nestedRepeat(sub, inside) {
			return true
		}
	}
	return false
}
