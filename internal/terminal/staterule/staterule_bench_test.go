package staterule

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// worstCaseScreen is a full 200-column, 50-row screen of lowercase text that
// almost matches the worst-case patterns everywhere.
func worstCaseScreen() Screen {
	row := strings.Repeat("abcdefghijklmnopqrstuvwxy ", 8)[:200]
	lines := make([]string, MaxLines)
	for i := range lines {
		lines[i] = row
	}
	return Screen{Lines: lines, CursorRow: MaxLines - 1, Title: row}
}

// worstCaseSet fills a set with as many copies of pattern as the limits let
// through, each over the largest region.
func worstCaseSet(tb testing.TB, pattern string) *Compiled {
	tb.Helper()
	var rules []Rule
	for i := 0; i < MaxRules; i++ {
		candidate := append(append([]Rule(nil), rules...), Rule{
			ID: ids.New(), Region: RegionBottom, Lines: MaxLines,
			Pattern: fmt.Sprintf("%s%d", pattern, i), State: base.SessionIdle,
		})
		if _, err := NewSet(candidate); err != nil {
			break
		}
		rules = candidate
	}
	set, err := NewSet(rules)
	if err != nil {
		tb.Fatal(err)
	}
	compiled, err := Compile(set)
	if err != nil {
		tb.Fatal(err)
	}
	return compiled
}

var worstCasePatterns = []string{`(?:[a-y]){1,30}zN`, `(?:[a-z]|\pL){1,40}zN`}

func BenchmarkMatchWorstCase(b *testing.B) {
	screen := worstCaseScreen()
	for _, pattern := range worstCasePatterns {
		compiled := worstCaseSet(b, pattern)
		b.Run(pattern, func(b *testing.B) {
			b.ReportMetric(float64(len(compiled.rules)), "rules")
			for b.Loop() {
				if _, ok := compiled.Match(screen); ok {
					b.Fatal("worst case matched")
				}
			}
		})
	}
}
