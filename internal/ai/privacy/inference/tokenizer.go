// Ported from Hugging Face tokenizers 0.23.1 (Apache-2.0) tokenizer/mod.rs,
// tokenizer/added_vocabulary.rs and processors/template.rs.
// Modified for NexTerm; see ../third_party/ASTRLINK_NOTICE.md.

package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type unsupportedTokenizerError string

func (e unsupportedTokenizerError) Error() string {
	return "unsupported tokenizer component: " + string(e)
}

func errUnsupportedTokenizer(component string) error { return unsupportedTokenizerError(component) }

// tokenizer implements exactly the subset of HF tokenizers 0.23.1 that the
// AstrLink Guard tokenizer.json uses: non-normalized added tokens, a
// precompiled-charsmap normalizer, Split/Metaspace pre-tokenizers, a Unigram
// model and a TemplateProcessing post-processor. Any other component fails
// closed at load time instead of being approximated.
type tokenizer struct {
	added      []addedToken
	normalizer []*precompiled
	pre        preTokenizer
	model      *unigram
	template   *template
}

type addedToken struct {
	id      uint32
	content string
	lstrip  bool
	rstrip  bool
}

type encoding struct {
	ids     []uint32
	tokens  []string
	offsets [][2]int
}

type templatePiece struct {
	special bool
	id      string
	typeID  uint32
}

type template struct {
	single  []templatePiece
	special map[string]specialTokens
}

type specialTokens struct {
	ids    []uint32
	tokens []string
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func loadTokenizer(data []byte) (*tokenizer, error) {
	var document struct {
		Version       string            `json:"version"`
		Truncation    json.RawMessage   `json:"truncation"`
		Padding       json.RawMessage   `json:"padding"`
		AddedTokens   []json.RawMessage `json:"added_tokens"`
		Normalizer    json.RawMessage   `json:"normalizer"`
		PreTokenizer  json.RawMessage   `json:"pre_tokenizer"`
		Model         json.RawMessage   `json:"model"`
		PostProcessor json.RawMessage   `json:"post_processor"`
		Decoder       json.RawMessage   `json:"decoder"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("invalid tokenizer.json: %w", err)
	}
	if document.Version != "1.0" {
		return nil, errUnsupportedTokenizer("version")
	}
	var modelType struct {
		Type         string   `json:"type"`
		UnknownID    *int     `json:"unk_id"`
		ByteFallback bool     `json:"byte_fallback"`
		Vocabulary   [][2]any `json:"vocab"`
	}
	if err := decodeStrict(document.Model, &modelType); err != nil || modelType.Type != "Unigram" {
		return nil, errUnsupportedTokenizer("model")
	}
	model, err := newUnigram(modelType.Vocabulary, modelType.UnknownID, modelType.ByteFallback)
	if err != nil {
		return nil, err
	}
	t := &tokenizer{model: model}
	for _, raw := range document.AddedTokens {
		var token struct {
			ID         uint32 `json:"id"`
			Content    string `json:"content"`
			SingleWord bool   `json:"single_word"`
			LStrip     bool   `json:"lstrip"`
			RStrip     bool   `json:"rstrip"`
			Normalized bool   `json:"normalized"`
			Special    bool   `json:"special"`
		}
		// Normalized and single-word added tokens need matching rules
		// that this model does not use.
		if err := decodeStrict(raw, &token); err != nil || token.Normalized || token.SingleWord {
			return nil, errUnsupportedTokenizer("added_tokens")
		}
		if token.Content == "" {
			continue
		}
		id, ok := model.ids[token.Content]
		if !ok || id != token.ID {
			return nil, errUnsupportedTokenizer("added_tokens")
		}
		t.added = append(t.added, addedToken{id, token.Content, token.LStrip, token.RStrip})
	}
	if t.normalizer, err = loadNormalizer(document.Normalizer); err != nil {
		return nil, err
	}
	if t.pre, err = loadPreTokenizer(document.PreTokenizer); err != nil {
		return nil, err
	}
	if t.template, err = loadTemplate(document.PostProcessor, model); err != nil {
		return nil, err
	}
	return t, nil
}

func isNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

func componentType(raw json.RawMessage) string {
	var header struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &header)
	return header.Type
}

func loadNormalizer(raw json.RawMessage) ([]*precompiled, error) {
	if isNull(raw) {
		return nil, nil
	}
	switch componentType(raw) {
	case "Sequence":
		var sequence struct {
			Type        string            `json:"type"`
			Normalizers []json.RawMessage `json:"normalizers"`
		}
		if err := decodeStrict(raw, &sequence); err != nil {
			return nil, errUnsupportedTokenizer("normalizer")
		}
		var result []*precompiled
		for _, item := range sequence.Normalizers {
			inner, err := loadNormalizer(item)
			if err != nil || inner == nil {
				return nil, errUnsupportedTokenizer("normalizer")
			}
			result = append(result, inner...)
		}
		return result, nil
	case "Precompiled":
		var value struct {
			Type     string `json:"type"`
			Charsmap string `json:"precompiled_charsmap"`
		}
		if err := decodeStrict(raw, &value); err != nil {
			return nil, errUnsupportedTokenizer("normalizer")
		}
		charsmap, err := base64.StdEncoding.DecodeString(value.Charsmap)
		if err != nil {
			return nil, errUnsupportedTokenizer("precompiled_charsmap")
		}
		normalizer, err := newPrecompiled(charsmap)
		if err != nil {
			return nil, err
		}
		return []*precompiled{normalizer}, nil
	default:
		return nil, errUnsupportedTokenizer("normalizer")
	}
}

func loadPreTokenizer(raw json.RawMessage) (preTokenizer, error) {
	if isNull(raw) {
		return sequencePreTokenizer(nil), nil
	}
	switch componentType(raw) {
	case "Sequence":
		var sequence struct {
			Type          string            `json:"type"`
			PreTokenizers []json.RawMessage `json:"pretokenizers"`
		}
		if err := decodeStrict(raw, &sequence); err != nil {
			return nil, errUnsupportedTokenizer("pre_tokenizer")
		}
		var result sequencePreTokenizer
		for _, item := range sequence.PreTokenizers {
			inner, err := loadPreTokenizer(item)
			if err != nil {
				return nil, err
			}
			result = append(result, inner)
		}
		return result, nil
	case "Split":
		var split struct {
			Type    string `json:"type"`
			Pattern struct {
				Regex  *string `json:"Regex"`
				String *string `json:"String"`
			} `json:"pattern"`
			Behavior string `json:"behavior"`
			Invert   bool   `json:"invert"`
		}
		if err := decodeStrict(raw, &split); err != nil {
			return nil, errUnsupportedTokenizer("pre_tokenizer Split")
		}
		behavior, ok := map[string]splitBehavior{
			"Removed": splitRemoved, "Isolated": splitIsolated, "MergedWithPrevious": splitMergedWithPrevious,
			"MergedWithNext": splitMergedWithNext, "Contiguous": splitContiguous,
		}[split.Behavior]
		if !ok {
			return nil, errUnsupportedTokenizer("pre_tokenizer Split behavior")
		}
		p := &splitPreTokenizer{behavior: behavior, invert: split.Invert}
		switch {
		case split.Pattern.Regex != nil && split.Pattern.String == nil:
			// Onig-only regexes are not translated to RE2 in general; each
			// supported pattern has an explicit implementation.
			switch *split.Pattern.Regex {
			case leadingSpacePattern:
				p.matches = func(s string) []patternMatch { return regexMatches(s, leadingSpaceMatches(s)) }
			case `\s`:
				p.matches = func(s string) []patternMatch { return predicateMatches(s, onigSpace) }
			default:
				return nil, errUnsupportedTokenizer("pre_tokenizer Split regex")
			}
		case split.Pattern.String != nil && split.Pattern.Regex == nil && *split.Pattern.String != "":
			literal := *split.Pattern.String
			p.matches = func(s string) []patternMatch { return regexMatches(s, literalMatches(s, literal)) }
		default:
			return nil, errUnsupportedTokenizer("pre_tokenizer Split pattern")
		}
		return p, nil
	case "Metaspace":
		var metaspace struct {
			Type           string  `json:"type"`
			Replacement    string  `json:"replacement"`
			AddPrefixSpace *bool   `json:"add_prefix_space"`
			PrependScheme  *string `json:"prepend_scheme"`
			Split          *bool   `json:"split"`
			StrRep         *string `json:"str_rep"`
		}
		if err := decodeStrict(raw, &metaspace); err != nil || utf8.RuneCountInString(metaspace.Replacement) != 1 {
			return nil, errUnsupportedTokenizer("pre_tokenizer Metaspace")
		}
		p := &metaspacePreTokenizer{prepend: "always", split: true}
		p.replacement, _ = utf8.DecodeRuneInString(metaspace.Replacement)
		if metaspace.PrependScheme != nil {
			p.prepend = *metaspace.PrependScheme
		}
		if p.prepend != "always" && p.prepend != "first" && p.prepend != "never" {
			return nil, errUnsupportedTokenizer("pre_tokenizer Metaspace prepend_scheme")
		}
		if metaspace.AddPrefixSpace != nil && !*metaspace.AddPrefixSpace {
			if p.prepend != "never" {
				return nil, errUnsupportedTokenizer("pre_tokenizer Metaspace add_prefix_space")
			}
		}
		if metaspace.Split != nil {
			p.split = *metaspace.Split
		}
		return p, nil
	default:
		return nil, errUnsupportedTokenizer("pre_tokenizer")
	}
}

func loadTemplate(raw json.RawMessage, model *unigram) (*template, error) {
	if isNull(raw) {
		return nil, nil
	}
	if componentType(raw) != "TemplateProcessing" {
		return nil, errUnsupportedTokenizer("post_processor")
	}
	type piece struct {
		SpecialToken *struct {
			ID     string `json:"id"`
			TypeID uint32 `json:"type_id"`
		} `json:"SpecialToken"`
		Sequence *struct {
			ID     string `json:"id"`
			TypeID uint32 `json:"type_id"`
		} `json:"Sequence"`
	}
	var document struct {
		Type          string  `json:"type"`
		Single        []piece `json:"single"`
		Pair          []piece `json:"pair"`
		SpecialTokens map[string]struct {
			ID     string   `json:"id"`
			IDs    []uint32 `json:"ids"`
			Tokens []string `json:"tokens"`
		} `json:"special_tokens"`
	}
	if err := decodeStrict(raw, &document); err != nil {
		return nil, errUnsupportedTokenizer("post_processor")
	}
	t := &template{special: make(map[string]specialTokens)}
	for name, special := range document.SpecialTokens {
		if special.ID != name || len(special.IDs) != len(special.Tokens) {
			return nil, errUnsupportedTokenizer("post_processor special_tokens")
		}
		for i, id := range special.IDs {
			if int(id) >= len(model.pieces) || model.pieces[id] != special.Tokens[i] {
				return nil, errUnsupportedTokenizer("post_processor special_tokens")
			}
		}
		t.special[name] = specialTokens{special.IDs, special.Tokens}
	}
	convert := func(pieces []piece, pair bool) ([]templatePiece, error) {
		var result []templatePiece
		sequences := map[string]bool{}
		for _, p := range pieces {
			switch {
			case p.SpecialToken != nil && p.Sequence == nil:
				if _, ok := t.special[p.SpecialToken.ID]; !ok {
					return nil, errUnsupportedTokenizer("post_processor template")
				}
				result = append(result, templatePiece{true, p.SpecialToken.ID, p.SpecialToken.TypeID})
			case p.Sequence != nil && p.SpecialToken == nil:
				if p.Sequence.ID != "A" && (!pair || p.Sequence.ID != "B") {
					return nil, errUnsupportedTokenizer("post_processor template")
				}
				sequences[p.Sequence.ID] = true
				result = append(result, templatePiece{false, p.Sequence.ID, p.Sequence.TypeID})
			default:
				return nil, errUnsupportedTokenizer("post_processor template")
			}
		}
		if !sequences["A"] || (pair && !sequences["B"]) || len(sequences) != map[bool]int{false: 1, true: 2}[pair] {
			return nil, errUnsupportedTokenizer("post_processor template")
		}
		return result, nil
	}
	var err error
	if t.single, err = convert(document.Single, false); err != nil {
		return nil, err
	}
	if _, err = convert(document.Pair, true); err != nil {
		return nil, err
	}
	sequences := 0
	for _, p := range t.single {
		if !p.special {
			sequences++
		}
	}
	if sequences != 1 {
		return nil, errUnsupportedTokenizer("post_processor template")
	}
	return t, nil
}

// window is a post-processed model input and the positions of its content.
type window struct {
	ids, attention, typeIDs []int64
	content                 []int
}

// specialTokenCount is the number of tokens the post-processor adds.
func (t *tokenizer) specialTokenCount() int {
	if t.template == nil {
		return 0
	}
	count := 0
	for _, p := range t.template.single {
		if p.special {
			count += len(t.template.special[p.id].ids)
		}
	}
	return count
}

// prepareWindow wraps encoding[start:end] like tokenizer.post_process(content,
// None, true) and records where the content tokens landed.
func (t *tokenizer) prepareWindow(e *encoding, start, end int) (*window, error) {
	if start >= end || end > len(e.ids) {
		return nil, errTokenizer
	}
	w := &window{}
	appendToken := func(id uint32, typeID uint32, content bool) {
		if content {
			w.content = append(w.content, len(w.ids))
		}
		w.ids = append(w.ids, int64(id))
		w.attention = append(w.attention, 1)
		w.typeIDs = append(w.typeIDs, int64(typeID))
	}
	if t.template == nil {
		for _, id := range e.ids[start:end] {
			appendToken(id, 0, true)
		}
		return w, nil
	}
	for _, p := range t.template.single {
		if p.special {
			for _, id := range t.template.special[p.id].ids {
				appendToken(id, p.typeID, false)
			}
			continue
		}
		for _, id := range e.ids[start:end] {
			appendToken(id, p.typeID, true)
		}
	}
	if len(w.content) != end-start {
		return nil, errTokenizer
	}
	return w, nil
}

// cancelCheckInterval bounds the work between context checks while
// tokenizing, so a slow path over a large input stays cancellable.
const cancelCheckInterval = 1024

// encode mirrors Tokenizer::encode(text, false) with byte offsets.
func (t *tokenizer) encode(ctx context.Context, text string) (*encoding, error) {
	type split struct {
		n      *normalizedString
		tokens []modelToken
		done   bool
	}
	root := newNormalizedString(text)
	var splits []split
	for _, m := range t.addedMatches(text) {
		part, ok := root.slice(m.start, m.end)
		if !ok {
			return nil, errTokenizer
		}
		if part.normalized == "" {
			continue
		}
		if m.added != nil {
			splits = append(splits, split{n: part, tokens: []modelToken{{m.added.id, part.normalized, 0, len(part.normalized)}}, done: true})
		} else {
			splits = append(splits, split{n: part})
		}
	}
	var pre []split
	for i, s := range splits {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if s.done {
			pre = append(pre, s)
			continue
		}
		for _, normalizer := range t.normalizer {
			if err := normalizer.normalize(ctx, s.n); err != nil {
				return nil, err
			}
		}
		// The normalized-token pass re-slices the whole normalized string,
		// which re-bases its original range.
		if s.n.normalized == "" {
			continue
		}
		whole, ok := s.n.slice(0, len(s.n.normalized))
		if !ok {
			return nil, errTokenizer
		}
		s.n = whole
		parts, err := t.pre.preTokenize([]*normalizedString{s.n})
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			pre = append(pre, split{n: part})
		}
	}
	e := &encoding{}
	for i, s := range pre {
		if i%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tokens := s.tokens
		if !s.done {
			tokens = t.model.tokenize(s.n.normalized)
		}
		shift, _ := s.n.originalOffsets()
		for _, token := range tokens {
			start, end := token.start, token.end
			if mappedStart, mappedEnd, ok := s.n.convertNormalized(token.start, token.end); ok {
				start, end = shift+mappedStart, shift+mappedEnd
			}
			e.ids = append(e.ids, token.id)
			e.tokens = append(e.tokens, token.value)
			e.offsets = append(e.offsets, [2]int{start, end})
		}
	}
	return e, nil
}

type addedMatch struct {
	start, end int
	added      *addedToken
}

// addedMatches splits text around added tokens like
// AddedVocabulary::find_matches with a leftmost-longest automaton.
func (t *tokenizer) addedMatches(text string) []addedMatch {
	if text == "" {
		return []addedMatch{{0, 0, nil}}
	}
	var result []addedMatch
	startOffset := 0
	for position := 0; position < len(text); {
		var best *addedToken
		for i := range t.added {
			token := &t.added[i]
			if strings.HasPrefix(text[position:], token.content) && (best == nil || len(token.content) > len(best.content)) {
				best = token
			}
		}
		if best == nil {
			position++
			continue
		}
		start, stop := position, position+len(best.content)
		// The automaton resumes after the raw match, not after stripped spaces.
		position = stop
		if best.lstrip {
			newStart := len(strings.TrimRightFunc(text[:start], onigSpace))
			start = max(newStart, startOffset)
		}
		if best.rstrip {
			stop += len(text[stop:]) - len(strings.TrimLeftFunc(text[stop:], onigSpace))
		}
		if startOffset < start {
			result = append(result, addedMatch{startOffset, start, nil})
		}
		result = append(result, addedMatch{start, stop, best})
		startOffset = stop
	}
	if startOffset != len(text) {
		result = append(result, addedMatch{startOffset, len(text), nil})
	}
	return result
}
