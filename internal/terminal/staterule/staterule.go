// Package staterule holds user-defined session state rules: "when this
// region of the screen matches this pattern, the session is in this state".
// Rules are user data, so the helper needs no per-program upkeep; they are
// validated here once and shared by the app, which stores them, and the
// durable helper, which applies them.
//
// Patterns are Go RE2 regular expressions, which match in time linear in
// the input: the work of one rule grows with its compiled program size times
// the bytes it is matched against. Both are capped per rule, the text of a
// bottom region at MaxLineBytes per row, and a set is limited by the sum of
// program size times region bytes over its rules (MaxSetCost), so evaluating
// a set at an idle transition does a bounded amount of work whatever the
// screen holds.
package staterule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Regions a rule can look at.
const (
	// RegionBottom is the bottom rows of the screen: Lines rows ending at
	// the lower of the cursor row and the last non-blank row.
	RegionBottom = "bottom"
	// RegionTitle is the window title set with OSC 0 or 2.
	RegionTitle = "title"
)

// Limits on a rule and a rule set.
const (
	// MaxRules bounds a rule set and the rules one owner may store.
	MaxRules = 64
	// MaxPatternBytes bounds a pattern's source.
	MaxPatternBytes = 512
	// MaxProgram bounds one compiled pattern, in RE2 instructions.
	MaxProgram = 1000
	// MaxSetProgram bounds the compiled patterns of a whole set.
	MaxSetProgram = 8000
	// MaxSetCost bounds the sum over a set's rules of program size times
	// the most bytes the rule's region can hold (RegionBytes).
	MaxSetCost = 4_000_000
	// MaxLines bounds the rows of a bottom region; DefaultLines is used
	// when a rule leaves Lines zero.
	MaxLines     = 50
	DefaultLines = 5
	// MaxKeysBytes bounds a rule's key sequence.
	MaxKeysBytes = 64
	// MaxLineBytes is what one row of a bottom region contributes at most:
	// a region of n rows is matched against at most its last n*MaxLineBytes
	// bytes. MaxTitleBytes bounds the title text.
	MaxLineBytes  = 256
	MaxTitleBytes = 1024
)

// RegionBytes returns the most bytes a normalized rule is matched against.
func RegionBytes(rule Rule) int {
	if rule.Region == RegionTitle {
		return MaxTitleBytes
	}
	return rule.Lines * MaxLineBytes
}

// Rule is one state rule as pushed to a helper. Rules are tried in order and
// the first match decides.
type Rule struct {
	ID      string `json:"id"`
	Region  string `json:"region"`
	Lines   int    `json:"lines,omitempty"`
	Pattern string `json:"pattern"`
	// State is the state a match puts the session in: needs_me,
	// just_finished, idle or running.
	State string `json:"state"`
	// Keys is an optional input sequence offered as a button while the rule
	// holds the session in needs_me. The helper never sends it by itself.
	Keys string `json:"keys,omitempty"`
}

// Set is an ordered rule set with its revision.
type Set struct {
	Revision string `json:"revision"`
	Rules    []Rule `json:"rules"`
}

// Error describes why a rule is invalid. Field is "id", "region", "lines",
// "pattern", "state", "keys", or "rules" for a limit of the whole set; Index
// is the rule's position in its set, or -1 for a single rule.
type Error struct {
	Index int
	Field string
	// Limit is the exceeded limit, zero when the error is not about one.
	Limit int
	Err   error
}

func (e *Error) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("state rule %d: %s: %v", e.Index+1, e.Field, e.Err)
	}
	return fmt.Sprintf("state rule: %s: %v", e.Field, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// ErrLimit marks an Error caused by a size or count limit.
var ErrLimit = errors.New("limit exceeded")

func limitError(index int, field string, limit int, format string, args ...any) *Error {
	return &Error{Index: index, Field: field, Limit: limit, Err: fmt.Errorf("%w: "+format, append([]any{ErrLimit}, args...)...)}
}

// TargetState reports whether state is one a rule may set.
func TargetState(state string) bool {
	switch state {
	case base.SessionNeedsMe, base.SessionJustFinished, base.SessionIdle, base.SessionRunning:
		return true
	}
	return false
}

// Normalize fills defaults: a bottom region without Lines gets DefaultLines
// and a title region drops Lines.
func Normalize(rule Rule) Rule {
	switch rule.Region {
	case RegionBottom:
		if rule.Lines == 0 {
			rule.Lines = DefaultLines
		}
	case RegionTitle:
		rule.Lines = 0
	}
	return rule
}

// Validate checks one normalized rule and returns its compiled program size.
func Validate(rule Rule) (int, error) {
	return validate(-1, rule)
}

func validate(index int, rule Rule) (int, error) {
	if !ids.Valid(rule.ID) {
		return 0, &Error{Index: index, Field: "id", Err: errors.New("not a valid ID")}
	}
	switch rule.Region {
	case RegionBottom:
		if rule.Lines < 1 || rule.Lines > MaxLines {
			return 0, limitError(index, "lines", MaxLines, "lines must be between 1 and %d", MaxLines)
		}
	case RegionTitle:
		if rule.Lines != 0 {
			return 0, &Error{Index: index, Field: "lines", Err: errors.New("a title rule has no lines")}
		}
	default:
		return 0, &Error{Index: index, Field: "region", Err: fmt.Errorf("unknown region %q", rule.Region)}
	}
	if !TargetState(rule.State) {
		return 0, &Error{Index: index, Field: "state", Err: fmt.Errorf("unknown target state %q", rule.State)}
	}
	if len(rule.Keys) > MaxKeysBytes {
		return 0, limitError(index, "keys", MaxKeysBytes, "keys exceed %d bytes", MaxKeysBytes)
	}
	if !utf8.ValidString(rule.Keys) {
		return 0, &Error{Index: index, Field: "keys", Err: errors.New("keys are not valid UTF-8")}
	}
	if rule.Keys != "" && rule.State != base.SessionNeedsMe {
		return 0, &Error{Index: index, Field: "keys", Err: errors.New("only a needs_me rule offers keys")}
	}
	size, err := programSize(rule.Pattern)
	if err != nil {
		if typed, ok := err.(*Error); ok {
			typed.Index = index
			return 0, typed
		}
		return 0, &Error{Index: index, Field: "pattern", Err: err}
	}
	return size, nil
}

// programSize compiles pattern as RE2 and returns its instruction count.
func programSize(pattern string) (int, error) {
	if strings.TrimSpace(pattern) == "" {
		return 0, errors.New("pattern is empty")
	}
	if len(pattern) > MaxPatternBytes {
		return 0, limitError(-1, "pattern", MaxPatternBytes, "pattern exceeds %d bytes", MaxPatternBytes)
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return 0, err
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return 0, err
	}
	if len(program.Inst) > MaxProgram {
		return 0, limitError(-1, "pattern", MaxProgram, "pattern compiles to %d instructions, more than %d", len(program.Inst), MaxProgram)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, err
	}
	// An empty match would make the rule match every screen.
	if re.MatchString("") {
		return 0, errors.New("pattern matches empty text")
	}
	return len(program.Inst), nil
}

// NewSet validates rules, normalized, and returns them as a set with its
// revision.
func NewSet(rules []Rule) (Set, error) {
	normalized := make([]Rule, len(rules))
	for i, rule := range rules {
		normalized[i] = Normalize(rule)
	}
	if err := validateSet(normalized); err != nil {
		return Set{}, err
	}
	return Set{Revision: Revision(normalized), Rules: normalized}, nil
}

func validateSet(rules []Rule) error {
	if len(rules) > MaxRules {
		return limitError(-1, "rules", MaxRules, "%d rules exceed the limit of %d", len(rules), MaxRules)
	}
	seen := make(map[string]struct{}, len(rules))
	total, cost := 0, 0
	for i, rule := range rules {
		size, err := validate(i, rule)
		if err != nil {
			return err
		}
		if _, dup := seen[rule.ID]; dup {
			return &Error{Index: i, Field: "id", Err: errors.New("duplicate rule ID")}
		}
		seen[rule.ID] = struct{}{}
		total += size
		cost += size * RegionBytes(rule)
	}
	if total > MaxSetProgram {
		return limitError(-1, "rules", MaxSetProgram, "patterns compile to %d instructions, more than %d", total, MaxSetProgram)
	}
	if cost > MaxSetCost {
		return limitError(-1, "rules", MaxSetCost, "patterns times region sizes come to %d, more than %d; use fewer rows or simpler patterns", cost, MaxSetCost)
	}
	return nil
}

// Revision identifies the content of an ordered rule list; it is empty for
// no rules. Two sides holding the same rules compute the same revision.
func Revision(rules []Rule) string {
	if len(rules) == 0 {
		return ""
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16])
}

// Compiled is a validated rule set ready to match.
type Compiled struct {
	revision string
	rules    []compiledRule
}

type compiledRule struct {
	Rule
	re *regexp.Regexp
}

// Match is the rule that matched a screen.
type Match struct {
	ID    string
	State string
	Keys  string
}

// Compile validates a set, including that its revision matches its rules,
// and compiles it. A set without rules compiles to nil.
func Compile(set Set) (*Compiled, error) {
	if err := validateSet(set.Rules); err != nil {
		return nil, err
	}
	if revision := Revision(set.Rules); revision != set.Revision {
		return nil, &Error{Index: -1, Field: "revision", Err: errors.New("revision does not match the rules")}
	}
	if len(set.Rules) == 0 {
		return nil, nil
	}
	compiled := &Compiled{revision: set.Revision, rules: make([]compiledRule, len(set.Rules))}
	for i, rule := range set.Rules {
		compiled.rules[i] = compiledRule{Rule: rule, re: regexp.MustCompile(rule.Pattern)}
	}
	return compiled, nil
}

// Revision returns the compiled set's revision; empty for nil.
func (c *Compiled) Revision() string {
	if c == nil {
		return ""
	}
	return c.revision
}

// Set returns the rules of the compiled set.
func (c *Compiled) Set() Set {
	if c == nil {
		return Set{}
	}
	rules := make([]Rule, len(c.rules))
	for i, rule := range c.rules {
		rules[i] = rule.Rule
	}
	return Set{Revision: c.revision, Rules: rules}
}

// Screen is what a rule set is matched against: the screen rows as
// terminal.Snapshot.Lines gives them, the cursor row and the window title.
type Screen struct {
	Lines     []string
	CursorRow int
	Title     string
}

// Match returns the first rule that matches the screen, or false.
func (c *Compiled) Match(screen Screen) (Match, bool) {
	if c == nil {
		return Match{}, false
	}
	var regions map[int]string
	title := clipTail(screen.Title, MaxTitleBytes)
	for _, rule := range c.rules {
		text := title
		if rule.Region == RegionBottom {
			if regions == nil {
				regions = make(map[int]string)
			}
			var ok bool
			if text, ok = regions[rule.Lines]; !ok {
				text = bottomText(screen, rule.Lines)
				regions[rule.Lines] = text
			}
		}
		if text != "" && rule.re.MatchString(text) {
			return Match{ID: rule.ID, State: rule.State, Keys: rule.Keys}, true
		}
	}
	return Match{}, false
}

// bottomText joins the bottom lines rows of the screen, ending at the lower
// of the cursor row and the last non-blank row.
func bottomText(screen Screen, lines int) string {
	end := min(screen.CursorRow, len(screen.Lines)-1)
	for i := len(screen.Lines) - 1; i > end; i-- {
		if strings.TrimFunc(screen.Lines[i], unicode.IsSpace) != "" {
			end = i
			break
		}
	}
	if end < 0 {
		return ""
	}
	start := max(0, end-lines+1)
	return clipTail(strings.Join(screen.Lines[start:end+1], "\n"), lines*MaxLineBytes)
}

// clipTail keeps the last limit bytes of text, cut at a rune start.
func clipTail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := len(text) - limit
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return text[cut:]
}
