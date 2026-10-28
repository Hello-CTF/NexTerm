package privacy

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
)

func TestDetectChunkedSplitsOversizedTextAndRebasesOffsets(t *testing.T) {
	const limit = 100
	detect := func(_ context.Context, text string) ([]Span, error) {
		if len(text) > limit {
			return nil, inference.ErrTokenLimit
		}
		var spans []Span
		for at := 0; ; {
			i := strings.Index(text[at:], "SECRET")
			if i < 0 {
				return spans, nil
			}
			spans = append(spans, Span{Start: at + i, End: at + i + 6, Label: "secret"})
			at += i + 6
		}
	}
	text := strings.Repeat("中文 line with SECRET inside\n", 40)
	spans, err := detectChunked(context.Background(), detect, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 40 {
		t.Fatalf("spans = %d, want 40", len(spans))
	}
	for _, span := range spans {
		if text[span.Start:span.End] != "SECRET" {
			t.Fatalf("span %+v covers %q", span, text[span.Start:span.End])
		}
	}
}
