package inference

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const syntheticTokenizer = `{
  "version": "1.0", "truncation": null, "padding": null,
  "added_tokens": [
    {"id": 0, "content": "<s>", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 1, "content": "<pad>", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 2, "content": "</s>", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 3, "content": "<unk>", "single_word": false, "lstrip": false, "rstrip": false, "normalized": false, "special": true},
    {"id": 10, "content": "<mask>", "single_word": false, "lstrip": true, "rstrip": false, "normalized": false, "special": true}
  ],
  "normalizer": null,
  "pre_tokenizer": {"type": "Sequence", "pretokenizers": [
    {"type": "Split", "pattern": {"Regex": "(?<![0-9\\s])\\s+|^\\s+"}, "behavior": "Removed", "invert": false},
    {"type": "Split", "pattern": {"Regex": "\\s"}, "behavior": "Isolated", "invert": false},
    {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "always", "split": true}
  ]},
  "post_processor": {"type": "TemplateProcessing",
    "single": [{"SpecialToken": {"id": "<s>", "type_id": 0}}, {"Sequence": {"id": "A", "type_id": 0}}, {"SpecialToken": {"id": "</s>", "type_id": 0}}],
    "pair": [{"SpecialToken": {"id": "<s>", "type_id": 0}}, {"Sequence": {"id": "A", "type_id": 0}}, {"SpecialToken": {"id": "</s>", "type_id": 0}}, {"Sequence": {"id": "B", "type_id": 0}}, {"SpecialToken": {"id": "</s>", "type_id": 0}}],
    "special_tokens": {"<s>": {"id": "<s>", "ids": [0], "tokens": ["<s>"]}, "</s>": {"id": "</s>", "ids": [2], "tokens": ["</s>"]}}},
  "decoder": {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "always", "split": true},
  "model": {"type": "Unigram", "unk_id": 3, "byte_fallback": false, "vocab": [
    ["<s>", 0.0], ["<pad>", 0.0], ["</s>", 0.0], ["<unk>", 0.0], ["▁", -2.0], ["▁secret", -1.0],
    ["你好", -1.0], ["▁你好", -1.5], ["a", -3.0], ["▁a", -2.0], ["<mask>", 0.0], ["1", -2.0]
  ]}
}`

func loadSynthetic(t *testing.T) *tokenizer {
	t.Helper()
	tok, err := loadTokenizer([]byte(syntheticTokenizer))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type tokenView struct {
	token string
	text  string
}

func encodeView(t *testing.T, tok *tokenizer, text string) []tokenView {
	t.Helper()
	e, err := tok.encode(text)
	if err != nil {
		t.Fatal(err)
	}
	views := make([]tokenView, len(e.ids))
	for i := range e.ids {
		views[i] = tokenView{e.tokens[i], text[e.offsets[i][0]:e.offsets[i][1]]}
	}
	return views
}

func TestTokenizerOffsetsSkipRemovedWhitespace(t *testing.T) {
	tok := loadSynthetic(t)
	e, err := tok.encode("你好 secret 你好 secret")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.offsets, [][2]int{{0, len("你好")}, {7, 13}, {14, 20}, {21, 27}}) || !slices.Equal(e.ids, []uint32{7, 5, 7, 5}) {
		t.Fatalf("got %v %v", e.ids, e.offsets)
	}
}

// The Guard split keeps whitespace after a digit and, because onig's ^ is a
// line anchor, removes whitespace that follows a newline inside that run.
func TestTokenizerLeadingSpaceSplitFollowsOniguruma(t *testing.T) {
	for _, tc := range []struct {
		text    string
		matches [][2]int
	}{
		{"a b", [][2]int{{1, 2}}},
		{" a", [][2]int{{0, 1}}},
		{"1 2", nil},
		{"1 \n  x", [][2]int{{3, 5}}},
		{"a\n\n b", [][2]int{{1, 4}}},
		{"1\u3000\u2028x", nil},
		{"x\u00a0\u3000y", [][2]int{{1, 6}}},
	} {
		if got := leadingSpaceMatches(tc.text); !slices.Equal(got, tc.matches) {
			t.Errorf("%q: got %v want %v", tc.text, got, tc.matches)
		}
	}
	tok := loadSynthetic(t)
	got := encodeView(t, tok, "1 a")
	want := []tokenView{{"▁", "1"}, {"1", "1"}, {"▁", " "}, {"▁a", "a"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestTokenizerAddedTokensStripLeftWhitespace(t *testing.T) {
	tok := loadSynthetic(t)
	got := encodeView(t, tok, "a  <mask>a</s>")
	want := []tokenView{{"▁a", "a"}, {"  <mask>", "  <mask>"}, {"▁a", "a"}, {"</s>", "</s>"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestTokenizerFusesUnknownCharacters(t *testing.T) {
	tok := loadSynthetic(t)
	e, err := tok.encode("😀😀a")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.ids, []uint32{4, 3, 8}) || !slices.Equal(e.offsets, [][2]int{{0, 4}, {0, 8}, {8, 9}}) {
		t.Fatalf("got %v %v %q", e.ids, e.offsets, e.tokens)
	}
}

func TestTokenizerTemplateWrapsEveryWindow(t *testing.T) {
	tok := loadSynthetic(t)
	e, err := tok.encode("你好 secret 你好 secret")
	if err != nil {
		t.Fatal(err)
	}
	if tok.specialTokenCount() != 2 {
		t.Fatalf("special tokens %d", tok.specialTokenCount())
	}
	ranges := chunkRanges(len(e.ids), 2, 1)
	if !slices.Equal(ranges, [][2]int{{0, 2}, {1, 3}, {2, 4}}) {
		t.Fatalf("ranges %v", ranges)
	}
	for _, r := range ranges {
		w, err := tok.prepareWindow(e, r[0], r[1])
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(w.ids, []int64{0, int64(e.ids[r[0]]), int64(e.ids[r[0]+1]), 2}) || !slices.Equal(w.content, []int{1, 2}) ||
			!slices.Equal(w.attention, []int64{1, 1, 1, 1}) || !slices.Equal(w.typeIDs, []int64{0, 0, 0, 0}) {
			t.Fatalf("window %+v", w)
		}
	}
}

func TestTokenizerRejectsUnsupportedComponents(t *testing.T) {
	for _, change := range [][2]string{
		{`"type": "Unigram"`, `"type": "BPE"`},
		{`"normalizer": null`, `"normalizer": {"type": "NFKC"}`},
		{`"\\s"}, "behavior": "Isolated"`, `"\\s+"}, "behavior": "Isolated"`},
		{`"(?<![0-9\\s])\\s+|^\\s+"`, `"(?<![0-9])\\s+"`},
		{`"prepend_scheme": "always", "split": true}
  ]}`, `"prepend_scheme": "sometimes", "split": true}
  ]}`},
		{`"lstrip": true, "rstrip": false, "normalized": false`, `"lstrip": true, "rstrip": false, "normalized": true`},
		{`{"type": "Split", "pattern": {"Regex": "\\s"}`, `{"type": "Whitespace"}, {"type": "Split", "pattern": {"Regex": "\\s"}`},
		{`"version": "1.0"`, `"version": "2.0"`},
	} {
		if !strings.Contains(syntheticTokenizer, change[0]) {
			t.Fatalf("fixture lacks %s", change[0])
		}
		if _, err := loadTokenizer([]byte(strings.Replace(syntheticTokenizer, change[0], change[1], 1))); err == nil {
			t.Errorf("accepted %s", change[1])
		}
	}
}

func TestGraphemeClusters(t *testing.T) {
	for text, want := range map[string][]string{
		"e\u0301x":                 {"e\u0301", "x"},
		"\r\n\n":                   {"\r\n", "\n"},
		"🇨🇳🇺":                      {"🇨🇳", "🇺"},
		"👩\u200d💻a":                {"👩\u200d💻", "a"},
		"©\u200d©":                 {"©\u200d©"},
		"a\u200d©":                 {"a\u200d", "©"},
		"\u0915\u094d\u0937":       {"\u0915\u094d\u0937"},
		"\u0915\u094d\u200d\u0937": {"\u0915\u094d\u200d\u0937"},
		"a\u0903b":                 {"a\u0903", "b"},
		"\u0600a":                  {"\u0600a"},
		"\u1100\u1161\u11a8":       {"\u1100\u1161\u11a8"},
		"a\u0300\u0301":            {"a\u0300\u0301"},
	} {
		var got []string
		forEachGrapheme(text, func(g string) { got = append(got, g) })
		if !slices.Equal(got, want) {
			t.Errorf("%q: got %q want %q", text, got, want)
		}
	}
}

// Opt-in: checks the downloaded AstrLink Guard tokenizer on edge cases.
// Every token offset must be an in-order UTF-8 span of the input.
func TestAstrLinkGuardTokenizerOffsets(t *testing.T) {
	directory := os.Getenv("NEXTERM_PRIVACY_TEST_MODEL")
	if directory == "" {
		t.Skip("real model path not configured")
	}
	data, err := os.ReadFile(filepath.Join(directory, "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := loadTokenizer(data)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{
		"ＡＢＣ\u3000１２３ ｶﾀｶﾅ", "e\u0301 cafe\u0301 Å", "™\x1eg", "\x00\x01 tab\tnew\nline\r\n", "<s> <mask> </s><unk>",
		"   leading and trailing   ", "1  2\n  3", "😀👩\u200d💻🇨🇳", "\u200b\u200d\ufeff", "ﬁ ﬀ ① ㈱",
		"客户张伟的邮箱是 zhang.wei@northwind-mail.net，手机号是13812345678。",
	}
	for _, file := range []string{"testdata/code-privacy-cases.json", "testdata/code-privacy-holdout.json"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var cases []privacyCase
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			texts = append(texts, c.Text)
		}
	}
	for _, text := range texts {
		e, err := tok.encode(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		previous := 0
		for i, o := range e.offsets {
			if o[0] < previous || o[0] > o[1] || o[1] > len(text) || !charBoundary(text, o[0]) || !charBoundary(text, o[1]) || int(e.ids[i]) >= len(tok.model.pieces) {
				t.Fatalf("%q: token %d %q offsets %v", text, i, e.tokens[i], o)
			}
			previous = o[0]
		}
	}
	e, err := tok.encode("ＡＢＣ")
	if err != nil || len(e.ids) == 0 {
		t.Fatal(err)
	}
	normalized := strings.Join(e.tokens, "")
	if normalized != "▁ABC" && !strings.HasPrefix(normalized, "▁") || !utf8.ValidString(normalized) {
		t.Fatalf("full-width normalization: %q", e.tokens)
	}
}
