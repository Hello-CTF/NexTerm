package highlight

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func rule(id, pattern string) Rule {
	return Rule{ID: id, Pattern: pattern, Foreground: "red", Enabled: true}
}

func encode(t *testing.T, settings Settings) []byte {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNormalizeAcceptsAndCanonicalizes(t *testing.T) {
	regex := rule("r2", `\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	regex.Regex = true
	regex.AssetIDs = []string{}
	raw := encode(t, Settings{Presets: []string{"errors", "future-preset"}, Rules: []Rule{rule("r1", "ERROR <x>"), regex}})
	got, err := Normalize(raw)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	var back Settings
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Rules) != 2 || back.Rules[0].Pattern != "ERROR <x>" || back.Rules[1].AssetIDs != nil {
		t.Fatalf("unexpected canonical value %s", got)
	}
	if strings.Contains(got, "u003c") {
		t.Fatalf("canonical value escapes HTML: %s", got)
	}
	again, err := Normalize([]byte(got))
	if err != nil || again != got {
		t.Fatalf("canonical value is not stable: %q, %v", again, err)
	}
	empty, err := Normalize([]byte(`{}`))
	if err != nil || empty != `{"presets":[],"rules":[]}` {
		t.Fatalf("empty settings = %q, %v", empty, err)
	}
}

func TestNormalizeRejects(t *testing.T) {
	regexRule := func(pattern string) Rule {
		r := rule("r1", pattern)
		r.Regex = true
		return r
	}
	noStyle := rule("r1", "x")
	noStyle.Foreground = ""
	badColor := rule("r1", "x")
	badColor.Background = "#ff0000"
	badAsset := rule("r1", "x")
	badAsset.AssetIDs = []string{"not-an-id"}
	many := make([]Rule, MaxRules+1)
	for i := range many {
		many[i] = rule(fmt.Sprintf("r%d", i), "x")
	}
	cases := map[string][]byte{
		"not json":           []byte(`[`),
		"unknown field":      []byte(`{"presets":[],"rules":[],"extra":1}`),
		"bad preset":         encode(t, Settings{Presets: []string{"Bad Preset"}}),
		"duplicate preset":   encode(t, Settings{Presets: []string{"errors", "errors"}}),
		"bad id":             encode(t, Settings{Rules: []Rule{rule("bad id", "x")}}),
		"duplicate id":       encode(t, Settings{Rules: []Rule{rule("a", "x"), rule("a", "y")}}),
		"empty pattern":      encode(t, Settings{Rules: []Rule{rule("a", "  ")}}),
		"long pattern":       encode(t, Settings{Rules: []Rule{rule("a", strings.Repeat("x", MaxPatternBytes+1))}}),
		"control char":       encode(t, Settings{Rules: []Rule{rule("a", "a\tb")}}),
		"invalid regex":      encode(t, Settings{Rules: []Rule{regexRule(`(`)}}),
		"empty match":        encode(t, Settings{Rules: []Rule{regexRule(`x*`)}}),
		"nested repeat":      encode(t, Settings{Rules: []Rule{regexRule(`(a+)+b`)}}),
		"repeat alternation": encode(t, Settings{Rules: []Rule{regexRule(`(?:foo|bar)+`)}}),
		"huge program":       encode(t, Settings{Rules: []Rule{regexRule(`(?:abc){400}`)}}),
		"no style":           encode(t, Settings{Rules: []Rule{noStyle}}),
		"bad color":          encode(t, Settings{Rules: []Rule{badColor}}),
		"bad asset":          encode(t, Settings{Rules: []Rule{badAsset}}),
		"too many rules":     encode(t, Settings{Rules: many}),
	}
	for name, raw := range cases {
		if got, err := Normalize(raw); err == nil {
			t.Errorf("%s: accepted as %s", name, got)
		}
	}
}

func TestNormalizeBoundsTheWholeSet(t *testing.T) {
	// Each rule is within the per-rule cap; together they exceed the set cap.
	rules := make([]Rule, 0, MaxRules)
	for i := 0; i < 20; i++ {
		r := rule(fmt.Sprintf("r%d", i), `[a-z]{450}q`)
		r.Regex = true
		rules = append(rules, r)
	}
	if size, err := ProgramSize(rules[0]); err != nil || size*len(rules) <= 8000 {
		t.Fatalf("test rule size %d, %v does not exceed the set cap", size, err)
	}
	if _, err := Normalize(encode(t, Settings{Rules: rules})); err == nil {
		t.Fatal("a set over the program cap was accepted")
	}
}

func TestPlainTextIsQuoted(t *testing.T) {
	plain := rule("r1", "a+(b")
	if _, err := ProgramSize(plain); err != nil {
		t.Fatalf("plain text with regex syntax rejected: %v", err)
	}
	if got := EffectivePattern(plain); got != `(?i)a\+\(b` {
		t.Fatalf("EffectivePattern = %q", got)
	}
	plain.CaseSensitive = true
	if got := EffectivePattern(plain); got != `a\+\(b` {
		t.Fatalf("EffectivePattern = %q", got)
	}
}
