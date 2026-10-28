package inference

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func contractSpan(start, end int, label string) Span {
	return Span{Start: start, End: end, Label: label, Score: 0.8125}
}

func ipSpan(t *testing.T, text, needle string) Span {
	t.Helper()
	start := strings.Index(text, needle)
	if start < 0 {
		t.Fatalf("needle %q", needle)
	}
	return contractSpan(start, start+len(needle), "ip_address")
}

func refined(t *testing.T, text string, spans []Span) []Span {
	t.Helper()
	output, err := spanContractRefine(text, spans)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func TestSpanContractConfigurationIsOptionalAndStrict(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   bool
		err    string
	}{
		{`{}`, false, ""},
		{`{"astrlink_guard_span_contract":null}`, false, ""},
		{`{"astrlink_guard_span_contract":"existing-ip-and-dynamic-url-v1"}`, true, ""},
		{`{"astrlink_guard_span_contract":"unknown"}`, false, "invalid_span_contract"},
		{`{"astrlink_guard_span_contract":"existing-ip-and-dynamic-url-v2"}`, false, "invalid_span_contract"},
		{`{"astrlink_guard_span_contract":1}`, false, "invalid_span_contract"},
		{`{"astrlink_guard_span_contract":{}}`, false, "invalid_span_contract"},
		{`[1`, false, "invalid_model_config"},
	} {
		got, err := configuredContract([]byte(tc.config), spanContractField, spanContract, "invalid_span_contract")
		if got != tc.want || (err == nil) != (tc.err == "") || (err != nil && err.Error() != tc.err) {
			t.Errorf("%s: got %v %v", tc.config, got, err)
		}
	}
}

func TestSpanContractKeepsByteOffsetsAndScores(t *testing.T) {
	text := "😀甲 IP=203.0.113.27:8443"
	start := strings.Index(text, "0.113")
	output := refined(t, text, []Span{contractSpan(start, start+5, "ip_address")})
	if text[output[0].Start:output[0].End] != "203.0.113.27" || output[0].Score != 0.8125 {
		t.Fatalf("got %+v", output)
	}
}

func TestSpanContractRejectsSpansOffUTF8Boundaries(t *testing.T) {
	text := "😀甲 IP=203.0.113.27"
	for _, invalid := range []Span{contractSpan(1, 2, "url"), contractSpan(0, 5, "ip_address"), contractSpan(4, 4, "ip_address"), contractSpan(4, len(text)+1, "ip_address")} {
		if _, err := spanContractRefine(text, []Span{invalid}); err == nil || err.Error() != "invalid_span_offsets" {
			t.Fatalf("%+v: %v", invalid, err)
		}
	}
	if output, err := spanContractRefine(text, nil); err != nil || len(output) != 0 {
		t.Fatalf("empty: %v %v", output, err)
	}
}

func TestSpanContractNarrowsIPSpans(t *testing.T) {
	for _, tc := range [][3]string{
		{"peer (203.0.113.209), ok", "(203.0.113.209),", "203.0.113.209"},
		{"peer ip:203.0.113.4 ok", "203.0.113", "203.0.113.4"},
		{"connect 198.51.100.7:8080 now", "198.51.100.7:8080", "198.51.100.7"},
		{"host 192.0.2.1.", "192.0.2.1.", "192.0.2.1"},
		{"host [2001:db8::1]:443 up", "[2001:db8::1]", "2001:db8::1"},
		{"gw fe80::1%eth0 up", "fe80::1%eth0", "fe80::1%eth0"},
		{"addr: 2001:db8::5 here", "2001:db8::5", "2001:db8::5"},
	} {
		output := refined(t, tc[0], []Span{ipSpan(t, tc[0], tc[1])})
		if len(output) != 1 || tc[0][output[0].Start:output[0].End] != tc[2] || output[0].Label != "ip_address" {
			t.Errorf("%s: %+v", tc[0], output)
		}
	}
}

func TestSpanContractLeavesNonIPAndAmbiguousSpans(t *testing.T) {
	for _, tc := range [][2]string{
		{"peer=203.0.113.209 ok", "=203.0.113.209"},
		{"gw 192.0.2.1%eth0 up", "192.0.2.1%eth0"},
		{"version 1.2.3.4 installed", "1.2.3.4"},
		{"see http://203.0.113.9/x now", "203.0.113.9"},
		{"value 999.1.1.1 end", "999.1.1.1"},
		{"a 203.0.113.1,203.0.113.2 b", "203.0.113.1,203.0.113.2"},
	} {
		original := ipSpan(t, tc[0], tc[1])
		if output := refined(t, tc[0], []Span{original}); !slices.Equal(output, []Span{original}) {
			t.Errorf("%s: %+v", tc[0], output)
		}
	}
}

func TestSpanContractNeverMovesIntoAnotherSpan(t *testing.T) {
	text := "x 203.0.113.5:9 y"
	start := strings.Index(text, "203")
	spans := []Span{contractSpan(start, start+11, "ip_address"), contractSpan(start+11, start+13, "account")}
	output := refined(t, text, spans)
	if output[1] != spans[1] || text[output[0].Start:output[0].End] != "203.0.113.5" {
		t.Fatalf("got %+v", output)
	}
}

func TestSpanContractSplitsDynamicURLCredentials(t *testing.T) {
	text := "nats://用户名:密😀码@%s:%d"
	output := refined(t, text, []Span{contractSpan(0, len(text), "url")})
	if len(output) != 2 || text[output[0].Start:output[0].End] != "用户名" || output[0].Label != "account" ||
		text[output[1].Start:output[1].End] != "密😀码" || output[1].Label != "common_secret" || output[1].Score != 0.8125 {
		t.Fatalf("got %+v", output)
	}
	for _, text := range []string{
		"nats://alice:password@%s?token=OTHER_SECRET",
		"nats://alice:password@%s/db#fragment",
		"nats://alice:password@db.internal:4222",
		"nats://alice:%s@%s",
	} {
		original := []Span{contractSpan(0, len(text), "url")}
		if output := refined(t, text, original); !slices.Equal(output, original) {
			t.Errorf("%s: %+v", text, output)
		}
	}
}

func TestCategoryCTableMatchesKnownPoints(t *testing.T) {
	for i := 1; i < len(categoryC); i++ {
		if categoryC[i-1][1] >= categoryC[i][0] {
			t.Fatal("table is not sorted")
		}
	}
	for r, want := range map[rune]bool{0: true, 0x200b: true, 0xe000: true, 'a': false, '密': false, '😀': false} {
		if categoryCRune(r) != want {
			t.Errorf("%U", r)
		}
	}
}

// The Rust regex crate's \s and nested classes have no direct RE2 spelling;
// the translation must keep Unicode whitespace and class unions.
func TestRustRegexTranslation(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   []string
		reject  []string
	}{
		{`\A(?:[[\s\x1c-\x1f]\"'` + "`" + `(\[{<:]*)\z`, []string{"", " \u3000\u00a0\x1c\"'`([{<:", "\u2028"}, []string{"a", "]"}},
		{`\A(?:[\][\s\x1c-\x1f]]*)\z`, []string{"]] \u2003"}, []string{"x"}},
		{`[^[\s\x1c-\x1f]<>"]+`, []string{"abc"}, []string{"\u3000", "<"}},
		{`(?i)\A(?:[•●]+|x{3,})\z`, []string{"•●", "XXX"}, []string{"xx"}},
	} {
		translated, err := translateRustRegex(tc.pattern)
		if err != nil {
			t.Fatalf("%s: %v", tc.pattern, err)
		}
		compiled := regexp.MustCompile(translated)
		for _, s := range tc.match {
			if !compiled.MatchString(s) {
				t.Errorf("%s should match %q", tc.pattern, s)
			}
		}
		for _, s := range tc.reject {
			if compiled.MatchString(s) {
				t.Errorf("%s should not match %q", tc.pattern, s)
			}
		}
	}
	for _, unsupported := range []string{`\w`, `\d`, `\b`, `[[^a]b]`, `[a&&b]`, `[[a]-z]`, `\p{L}`} {
		if _, err := translateRustRegex(unsupported); err == nil {
			t.Errorf("%s translated", unsupported)
		}
	}
	if _, err := loadSpanPatterns(); err != nil {
		t.Fatal(err)
	}
}

type oracleSpan struct {
	Start  int     `json:"start"`
	End    int     `json:"end"`
	Label  string  `json:"label"`
	Score  float32 `json:"score"`
	TextID int     `json:"text_id"`
}

func toSpans(values []oracleSpan) []Span {
	spans := make([]Span, len(values))
	for i, v := range values {
		spans[i] = Span{Start: v.Start, End: v.End, Label: v.Label, Score: v.Score}
	}
	return spans
}

// A synthetic subset of the AstrLink Guard runs/span-worker-contract-v1
// oracle whose expected spans come from the Python reference contracts.
func TestSpanContractConformsToSyntheticGuardOracle(t *testing.T) {
	file, err := os.Open("testdata/span-contract-v1-synthetic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 16<<20)
	scanner.Scan()
	var meta struct {
		Meta struct {
			Cases int `json:"cases"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	cases, changed := 0, 0
	for scanner.Scan() {
		var c struct {
			ID         string  `json:"id"`
			Text       *string `json:"text"`
			TextRepeat *struct {
				Unit  string `json:"unit"`
				Count int    `json:"count"`
				Tail  string `json:"tail"`
			} `json:"text_repeat"`
			Spans    []oracleSpan `json:"spans"`
			Expected []oracleSpan `json:"expected"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		var text string
		if c.Text != nil {
			text = *c.Text
		} else {
			text = strings.Repeat(c.TextRepeat.Unit, c.TextRepeat.Count) + c.TextRepeat.Tail
		}
		input, expected := toSpans(c.Spans), toSpans(c.Expected)
		if !slices.Equal(input, expected) {
			changed++
		}
		output, err := spanContractRefine(text, input)
		if err != nil || !slices.Equal(output, expected) {
			t.Errorf("%s: got %+v %v, want %+v", c.ID, output, err, expected)
		}
		cases++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if cases != meta.Meta.Cases || changed <= 500 {
		t.Fatalf("cases %d/%d, changed %d", cases, meta.Meta.Cases, changed)
	}
}
