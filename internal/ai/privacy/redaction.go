package privacy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Span uses UTF-8 byte offsets into the original, unnormalized text.
type Span struct {
	Start int     `json:"start"`
	End   int     `json:"end"`
	Label string  `json:"label"`
	Score float32 `json:"score"`
}

type Detector interface {
	Detect(context.Context, string) ([]Span, error)
}

// Exchange belongs to one provider request, including its retries. It must never
// be shared between users or retained in logs, settings, or conversation storage.
type Exchange struct {
	detector        Detector
	values          map[string]string
	markers         map[string]string
	allocator       *placeholderAllocator
	restorer        *TextRestorer
	numberAllocator *placeholderAllocator
	numbers         map[string]json.Number
	// known lists redacted values longest first for literal propagation.
	known      []string
	detections map[[32]byte][]Span
	identity   string
	shared     bool
}

// minPropagatedValueBytes keeps literal propagation from rewriting ordinary
// words that happen to equal a very short finding. Detected spans themselves
// are always replaced regardless of length.
const minPropagatedValueBytes = 4

func NewExchange(detector Detector) (*Exchange, error) {
	if detector == nil {
		return nil, errors.New("privacy detector is unavailable")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	return NewScopedExchange(detector, key)
}

// NewScopedExchange uses a stable, secret conversation key while keeping the
// restoration dictionary request-local. Markers do not depend on traversal order.
func NewScopedExchange(detector Detector, key [32]byte) (*Exchange, error) {
	if detector == nil {
		return nil, errors.New("privacy detector is unavailable")
	}
	if key == ([32]byte{}) {
		return nil, errors.New("privacy scope key is empty")
	}
	e := &Exchange{detector: detector, allocator: newPlaceholderAllocator(key), values: make(map[string]string), markers: make(map[string]string), detections: make(map[[32]byte][]Span)}
	if identified, ok := detector.(detectorIdentity); ok {
		e.identity = identified.detectionIdentity()
		e.shared = true
	}
	return e, nil
}

// detect reuses findings for text already inspected by this exchange and, for
// detectors with a stable identity, by earlier requests in the same scope.
func (e *Exchange) detect(ctx context.Context, text string) ([]Span, error) {
	key := detectionKey(e.allocator.key, e.identity, text)
	if spans, ok := e.detections[key]; ok {
		return slices.Clone(spans), nil
	}
	if e.shared {
		if spans, ok := sharedDetections.get(key); ok {
			e.detections[key] = spans
			return slices.Clone(spans), nil
		}
	}
	spans, err := e.detector.Detect(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("privacy detection failed: %w", err)
	}
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start || span.End > len(text) || !utf8.RuneStart(text[span.Start]) || (span.End < len(text) && !utf8.RuneStart(text[span.End])) {
			return nil, errors.New("privacy detector returned invalid offsets")
		}
	}
	spans = slices.Clone(spans)
	e.detections[key] = spans
	if e.shared {
		sharedDetections.put(key, spans)
	}
	return slices.Clone(spans), nil
}

// Redacted reports whether any value has been replaced by a placeholder.
func (e *Exchange) Redacted() bool { return len(e.values) > 0 }

func (e *Exchange) knownValues() []string {
	if e.known == nil {
		e.known = make([]string, 0, len(e.markers))
		for value := range e.markers {
			if len(value) >= minPropagatedValueBytes {
				e.known = append(e.known, value)
			}
		}
		sort.Slice(e.known, func(i, j int) bool {
			if len(e.known[i]) != len(e.known[j]) {
				return len(e.known[i]) > len(e.known[j])
			}
			return e.known[i] < e.known[j]
		})
	}
	return e.known
}

// propagate adds every occurrence of a value already known to the exchange
// that the detector did not report for this text. Longer values win, claimed
// ranges and existing placeholders are never split.
func (e *Exchange) propagate(text string, claimed []Span) []Span {
	known := e.knownValues()
	if len(known) == 0 {
		return claimed
	}
	covered := make([]bool, len(text))
	for _, span := range claimed {
		for i := span.Start; i < span.End; i++ {
			covered[i] = true
		}
	}
	free := func(start, end int) bool {
		for i := start; i < end; i++ {
			if covered[i] {
				return false
			}
		}
		return true
	}
	for _, value := range known {
		for offset := 0; offset+len(value) <= len(text); {
			at := strings.Index(text[offset:], value)
			if at < 0 {
				break
			}
			start, end := offset+at, offset+at+len(value)
			if free(start, end) && utf8.RuneStart(text[start]) && (end == len(text) || utf8.RuneStart(text[end])) && !isTokenPlaceholderSpan(text, start, end) {
				marker := e.markers[value]
				claimed = append(claimed, Span{Start: start, End: end, Label: string(e.allocator.used[marker].kind)})
				for i := start; i < end; i++ {
					covered[i] = true
				}
				offset = end
				continue
			}
			offset = start + 1
		}
	}
	sort.Slice(claimed, func(i, j int) bool { return claimed[i].Start < claimed[j].Start })
	return claimed
}

func (e *Exchange) Protect(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if text == "" {
		return text, nil
	}
	if !utf8.ValidString(text) {
		return "", errors.New("privacy input is not valid UTF-8")
	}
	spans, err := e.detect(ctx, text)
	if err != nil {
		return "", err
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start == spans[j].Start {
			if spans[i].End == spans[j].End {
				return spans[i].Label < spans[j].Label
			}
			return spans[i].End > spans[j].End
		}
		return spans[i].Start < spans[j].Start
	})
	// Union overlapping entities so no part of a detected secret escapes.
	merged := make([]Span, 0, len(spans))
	for _, span := range spans {
		if isTokenPlaceholderSpan(text, span.Start, span.End) {
			continue
		}
		if len(merged) > 0 && span.Start < merged[len(merged)-1].End {
			merged[len(merged)-1].End = max(merged[len(merged)-1].End, span.End)
		} else {
			merged = append(merged, span)
		}
	}
	// Register detected values first so the same text also propagates them.
	for _, span := range merged {
		if _, err := e.textMarker(span.Label, text[span.Start:span.End]); err != nil {
			return "", err
		}
	}
	merged = e.propagate(text, merged)
	var out strings.Builder
	previous := 0
	for _, span := range merged {
		out.WriteString(text[previous:span.Start])
		value := text[span.Start:span.End]
		marker, err := e.textMarker(span.Label, value)
		if err != nil {
			return "", err
		}
		out.WriteString(marker)
		previous = span.End
	}
	out.WriteString(text[previous:])
	return out.String(), nil
}

func (e *Exchange) textMarker(label, value string) (string, error) {
	marker, err := e.allocator.allocate(Kind(label), value)
	if err != nil {
		return "", err
	}
	if _, exists := e.values[marker]; !exists {
		e.values[marker] = value
		if _, known := e.markers[value]; !known {
			e.markers[value] = marker
			e.known = nil
		}
		e.restorer = nil
	}
	return marker, nil
}

func (e *Exchange) textRestorer() *TextRestorer {
	if e.restorer == nil {
		redactions := make([]Redaction, 0, len(e.values))
		for placeholder, value := range e.values {
			redactions = append(redactions, Redaction{Placeholder: placeholder, Value: value})
		}
		sort.Slice(redactions, func(i, j int) bool { return redactions[i].Placeholder < redactions[j].Placeholder })
		e.restorer = NewTextRestorer(redactions, ValuePlain)
	}
	return e.restorer
}

func (e *Exchange) Restore(text string) string {
	restored, _, _ := e.textRestorer().Restore(text, true)
	return restored
}

// TransformJSON changes string values, never structural keys. Decoding before
// restoration ensures quotes, backslashes and control characters stay escaped.
func TransformJSON(raw string, transform func(string) (string, error)) (string, error) {
	return transformJSON(raw, func(value string) (any, error) { return transform(value) }, nil)
}

func transformJSON(raw string, transform func(string) (any, error), checkKey func(string) error) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrUnsafeInput
	}
	value, duplicate, err := parsePrivacyJSON([]byte(raw), true)
	if err != nil {
		return "", err
	}
	if duplicate {
		return "", errors.New("privacy refuses duplicate JSON keys")
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch v := v.(type) {
		case string:
			return transform(v)
		case []any:
			for i, item := range v {
				next, err := walk(item)
				if err != nil {
					return nil, err
				}
				v[i] = next
			}
			return v, nil
		case map[string]any:
			for key, item := range v {
				if checkKey != nil {
					if err := checkKey(key); err != nil {
						return nil, err
					}
				}
				next, err := walk(item)
				if err != nil {
					return nil, err
				}
				v[key] = next
			}
			return v, nil
		default:
			return v, nil
		}
	}
	value, err = walk(value)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

// Restorer holds incomplete markers across stream boundaries. Inserted secrets
// are never interpreted again, even if they contain marker-shaped text.
type Restorer struct {
	restorer *TextRestorer
	pending  string
	left     string
}

func (e *Exchange) NewRestorer() *Restorer { return &Restorer{restorer: e.textRestorer()} }

func (r *Restorer) Push(text string, final bool) string {
	input := r.left + r.pending + text
	restored, _, hold := r.restorer.restoreFrom(input, final, len(r.left))
	r.pending = input[len(input)-hold:]
	consumed := input[:len(input)-hold]
	if len(consumed) > 0 {
		r.left = ""
		last := consumed[len(consumed)-1]
		if isASCIIAlphanumeric(last) {
			r.left = string(last)
		} else if last == '\\' && precedingBackslashes(consumed, len(consumed))%2 == 1 {
			r.left = "\\"
		}
	}
	return restored
}
