package staterule

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func rule(pattern, state string) Rule {
	return Rule{ID: ids.New(), Region: RegionBottom, Pattern: pattern, State: state}
}

func TestNewSetNormalizesAndRevisionsDeterministically(t *testing.T) {
	first := rule(`esc to interrupt`, base.SessionRunning)
	second := Rule{ID: ids.New(), Region: RegionTitle, Lines: 7, Pattern: `^✳`, State: base.SessionNeedsMe, Keys: "y"}
	set, err := NewSet([]Rule{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if set.Rules[0].Lines != DefaultLines || set.Rules[1].Lines != 0 {
		t.Fatalf("normalized lines = %d, %d", set.Rules[0].Lines, set.Rules[1].Lines)
	}
	again, err := NewSet([]Rule{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if set.Revision == "" || set.Revision != again.Revision {
		t.Fatalf("revisions %q and %q", set.Revision, again.Revision)
	}
	swapped, err := NewSet([]Rule{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if swapped.Revision == set.Revision {
		t.Fatal("reordered rules kept the revision")
	}
	empty, err := NewSet(nil)
	if err != nil || empty.Revision != "" {
		t.Fatalf("empty set = %+v, %v", empty, err)
	}
}

func TestValidateRejectsInvalidAndOversizedRules(t *testing.T) {
	cases := []struct {
		name  string
		rule  Rule
		field string
		limit bool
	}{
		{"bad id", Rule{ID: "x", Region: RegionBottom, Lines: 1, Pattern: "a", State: base.SessionIdle}, "id", false},
		{"region", Rule{ID: ids.New(), Region: "screen", Pattern: "a", State: base.SessionIdle}, "region", false},
		{"lines", Rule{ID: ids.New(), Region: RegionBottom, Lines: MaxLines + 1, Pattern: "a", State: base.SessionIdle}, "lines", true},
		{"state", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: "a", State: base.SessionExited}, "state", false},
		{"syntax", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: "(a", State: base.SessionIdle}, "pattern", false},
		{"backreference", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: `(a)\1`, State: base.SessionIdle}, "pattern", false},
		{"empty", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: "  ", State: base.SessionIdle}, "pattern", false},
		{"matches empty", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: "a*", State: base.SessionIdle}, "pattern", false},
		{"long pattern", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: strings.Repeat("a", MaxPatternBytes+1), State: base.SessionIdle}, "pattern", true},
		{"large program", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: `(\w{1,100}x){1,5}`, State: base.SessionIdle}, "pattern", true},
		{"keys on idle", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: "a", State: base.SessionIdle, Keys: "y"}, "keys", false},
		{"long keys", Rule{ID: ids.New(), Region: RegionBottom, Lines: 1, Pattern: "a", State: base.SessionNeedsMe, Keys: strings.Repeat("y", MaxKeysBytes+1)}, "keys", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(tc.rule)
			var typed *Error
			if !errors.As(err, &typed) || typed.Field != tc.field {
				t.Fatalf("Validate = %v, want a %s error", err, tc.field)
			}
			if errors.Is(err, ErrLimit) != tc.limit || (tc.limit && typed.Limit == 0) {
				t.Fatalf("limit error = %v (limit %d), want %v", err, typed.Limit, tc.limit)
			}
		})
	}
}

func TestNewSetBoundsCountAndTotalProgram(t *testing.T) {
	many := make([]Rule, MaxRules+1)
	for i := range many {
		many[i] = rule("a", base.SessionIdle)
	}
	if _, err := NewSet(many); !errors.Is(err, ErrLimit) {
		t.Fatalf("too many rules = %v", err)
	}
	// Each pattern fits on its own; together they exceed the set budget.
	heavy := make([]Rule, MaxRules)
	for i := range heavy {
		heavy[i] = rule(`\w{1,100}z`, base.SessionIdle)
	}
	if _, err := NewSet(heavy); !errors.Is(err, ErrLimit) {
		t.Fatalf("heavy set = %v", err)
	}
	duplicate := rule("a", base.SessionIdle)
	var typed *Error
	if _, err := NewSet([]Rule{duplicate, duplicate}); !errors.As(err, &typed) || typed.Field != "id" || typed.Index != 1 {
		t.Fatalf("duplicate = %v", err)
	}
}

func TestCompileRejectsAForeignRevision(t *testing.T) {
	set, err := NewSet([]Rule{rule("a", base.SessionIdle)})
	if err != nil {
		t.Fatal(err)
	}
	set.Revision = "0000"
	if _, err := Compile(set); err == nil {
		t.Fatal("a set whose revision does not match its rules compiled")
	}
	none, err := Compile(Set{})
	if err != nil || none != nil || none.Revision() != "" {
		t.Fatalf("empty set = %v, %v", none, err)
	}
	if _, ok := none.Match(Screen{Lines: []string{"a"}}); ok {
		t.Fatal("nil rule set matched")
	}
}

func TestMatchRegionsAndOrder(t *testing.T) {
	title := Rule{ID: ids.New(), Region: RegionTitle, Pattern: `^✳ `, State: base.SessionNeedsMe, Keys: "\r"}
	bottom := Rule{ID: ids.New(), Region: RegionBottom, Lines: 2, Pattern: `(?i)esc to interrupt`, State: base.SessionRunning}
	wide := Rule{ID: ids.New(), Region: RegionBottom, Lines: 10, Pattern: `Thinking`, State: base.SessionIdle}
	set, err := NewSet([]Rule{title, bottom, wide})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := Compile(set)
	if err != nil {
		t.Fatal(err)
	}
	screen := Screen{
		Lines:     []string{"Thinking", "a", "b", "c", "Press ESC to interrupt", "", "", ""},
		CursorRow: 1,
	}
	// The region ends at the last non-blank row below the cursor.
	match, ok := compiled.Match(screen)
	if !ok || match.ID != bottom.ID || match.State != base.SessionRunning {
		t.Fatalf("match = %+v, %v", match, ok)
	}
	// The title rule comes first once a title is set.
	screen.Title = "✳ Claude"
	if match, ok = compiled.Match(screen); !ok || match.ID != title.ID || match.Keys != "\r" {
		t.Fatalf("title match = %+v, %v", match, ok)
	}
	// Two rows above the end do not reach "Thinking"; ten do.
	screen.Title = ""
	screen.Lines[4] = "done"
	if match, ok = compiled.Match(screen); !ok || match.ID != wide.ID {
		t.Fatalf("wide match = %+v, %v", match, ok)
	}
	screen.Lines[0] = ""
	if match, ok = compiled.Match(screen); ok {
		t.Fatalf("unexpected match %+v", match)
	}
}

func compileRules(t *testing.T, rules ...Rule) *Compiled {
	t.Helper()
	set, err := NewSet(rules)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := Compile(set)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestMatchClipsLongRows(t *testing.T) {
	compiled := compileRules(t, Rule{ID: ids.New(), Region: RegionBottom, Lines: 2, Pattern: `END`, State: base.SessionIdle})
	// Only the first MaxLineBytes of a row are matched.
	long := strings.Repeat("é", MaxLineBytes) + "END"
	if _, ok := compiled.Match(Screen{Lines: []string{"", long}, CursorRow: 1}); ok {
		t.Fatal("text beyond the row byte limit was matched")
	}
	if _, ok := compiled.Match(Screen{Lines: []string{"", "short END"}, CursorRow: 1}); !ok {
		t.Fatal("short row did not match")
	}
}

// A dialog drawn with box characters fills each row with multi-byte
// borders; every row of the region still contributes its beginning.
func TestMatchSeesEveryRowOfABoxDrawnRegion(t *testing.T) {
	compiled := compileRules(t, Rule{ID: ids.New(), Region: RegionBottom, Lines: 5, Pattern: `╭─ Confirm`, State: base.SessionNeedsMe})
	const width = 200
	border := func(left, title, right string) string {
		inner := width - 2 - utf8.RuneCountInString(title)
		return left + title + strings.Repeat("─", inner) + right
	}
	row := func(text string) string {
		return "│" + text + strings.Repeat(" ", width-2-utf8.RuneCountInString(text)) + "│"
	}
	lines := []string{
		border("╭", "─ Confirm ", "╮"),
		row(" Do you want to proceed?"),
		row(" ❯ 1. Yes"),
		row("   2. No"),
		border("╰", "", "╯"),
	}
	if total := len(strings.Join(lines, "\n")); total <= 5*MaxLineBytes {
		t.Fatalf("the region is only %d bytes; the test needs a wider box", total)
	}
	if _, ok := compiled.Match(Screen{Lines: lines, CursorRow: 4}); !ok {
		t.Fatal("the top row of a box-drawn region was not matched")
	}
}

func TestRegionTextIsWhatARuleSees(t *testing.T) {
	screen := Screen{Lines: []string{"build log", "", "Continue? [y/N]", "", ""}, CursorRow: 2, Title: "✳ agent"}
	bottom := Normalize(Rule{Region: RegionBottom, Lines: 2})
	if got := RegionText(screen, bottom); got != "\nContinue? [y/N]" {
		t.Fatalf("bottom region = %q", got)
	}
	title := Normalize(Rule{Region: RegionTitle})
	if got := RegionText(screen, title); got != "✳ agent" {
		t.Fatalf("title region = %q", got)
	}
}
