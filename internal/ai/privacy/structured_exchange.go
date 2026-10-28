package privacy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ProtectJSON follows AstrLink's structured literal policy: numeric findings
// cover the complete number and become quoted markers, preserving valid JSON.
func (e *Exchange) ProtectJSON(ctx context.Context, raw string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !utf8.ValidString(raw) {
		return "", ErrUnsafeInput
	}
	_, duplicate, err := parsePrivacyJSON([]byte(raw), false)
	if err != nil {
		return "", err
	}
	if duplicate {
		return "", errors.New("privacy refuses duplicate JSON keys")
	}
	spans, err := e.detector.Detect(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("privacy detection failed: %w", err)
	}
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start || span.End > len(raw) || !utf8.RuneStart(raw[span.Start]) || (span.End < len(raw) && !utf8.RuneStart(raw[span.End])) {
			return "", errors.New("privacy detector returned invalid offsets")
		}
	}
	scanned := scanStructuredJSONLiterals(raw)
	var aligned []Span
	for _, span := range spans {
		aligned = scanned.align(aligned, span)
	}
	sort.Slice(aligned, func(i, j int) bool {
		if aligned[i].Start != aligned[j].Start {
			return aligned[i].Start < aligned[j].Start
		}
		if aligned[i].End != aligned[j].End {
			return aligned[i].End > aligned[j].End
		}
		return aligned[i].Label < aligned[j].Label
	})
	merged := make([]Span, 0, len(aligned))
	for _, span := range aligned {
		index := sort.Search(len(scanned.literals), func(i int) bool { return scanned.literals[i].end > span.Start })
		if index == len(scanned.literals) {
			continue
		}
		if scanned.literals[index].key {
			return "", errors.New("隐私保护检测到敏感 JSON 键，无法安全改写工具参数")
		}
		if isTokenPlaceholderSpan(raw, span.Start, span.End) {
			continue
		}
		if len(merged) > 0 && span.Start < merged[len(merged)-1].End {
			merged[len(merged)-1].End = max(merged[len(merged)-1].End, span.End)
		} else {
			merged = append(merged, span)
		}
	}
	var out strings.Builder
	previous := 0
	for _, span := range merged {
		value := raw[span.Start:span.End]
		var replacement string
		if scanned.isNumber(span.Start, span.End) {
			marker, err := e.numberMarker(span.Label, value)
			if err != nil {
				return "", err
			}
			quoted, err := json.Marshal(marker)
			if err != nil {
				return "", err
			}
			replacement = string(quoted)
		} else {
			var decoded string
			if err := json.Unmarshal([]byte(`"`+value+`"`), &decoded); err != nil {
				return "", ErrUnsafeInput
			}
			marker, err := e.textMarker(span.Label, decoded)
			if err != nil {
				return "", err
			}
			replacement = marker
		}
		out.WriteString(raw[previous:span.Start])
		out.WriteString(replacement)
		previous = span.End
	}
	out.WriteString(raw[previous:])
	return transformJSON(out.String(), func(value string) (any, error) { return e.Protect(ctx, value) }, func(key string) error {
		protected, err := e.Protect(ctx, key)
		if err != nil {
			return err
		}
		if protected != key {
			return errors.New("隐私保护检测到敏感 JSON 键，无法安全改写工具参数")
		}
		return nil
	})
}

func (e *Exchange) numberMarker(label, value string) (string, error) {
	if e.numberAllocator == nil {
		mac := hmac.New(sha256.New, e.allocator.key[:])
		mac.Write([]byte("nexterm/privacy/json-number/v1"))
		var key [32]byte
		copy(key[:], mac.Sum(nil))
		e.numberAllocator = newPlaceholderAllocator(key)
		// A shared collision ledger also covers text allocations in this request.
		e.numberAllocator.used = e.allocator.used
		e.numbers = make(map[string]json.Number)
	}
	marker, err := e.numberAllocator.allocate(Kind(label), value)
	if err != nil {
		return "", err
	}
	e.numbers[marker] = json.Number(value)
	e.values[marker] = value
	e.restorer = nil
	return marker, nil
}

// RestoreJSON restores numeric markers as JSON numbers only when the complete
// string is that marker. A marker embedded in a command remains a string.
func (e *Exchange) RestoreJSON(raw string) (string, error) {
	redactions := make([]Redaction, 0, len(e.numbers))
	for marker := range e.numbers {
		redactions = append(redactions, Redaction{Placeholder: marker, Value: ""})
	}
	probe := NewTextRestorer(redactions, ValuePlain)
	return transformJSON(raw, func(value string) (any, error) {
		restored := e.Restore(value)
		remaining, count, _ := probe.Restore(value, true)
		if remaining == "" && count == 1 {
			return json.Number(restored), nil
		}
		return restored, nil
	}, func(key string) error {
		if e.Restore(key) != key {
			return errors.New("AI 工具参数包含需要还原的 JSON 键，无法安全改写")
		}
		return nil
	})
}
