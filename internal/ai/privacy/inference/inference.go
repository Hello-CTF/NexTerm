// Package inference runs AstrLink's privacy engine in-process: a Go port of the
// AstrLink privacy worker that executes the downloaded ONNX model with the
// pure-Go gomlx backend, so no native runtime library is shipped.
package inference

import (
	"context"
	"errors"
	"math"
	"unicode/utf8"
)

const MinimumScore float32 = 0.8

type Span struct {
	Start int     `json:"start"`
	End   int     `json:"end"`
	Label string  `json:"label"`
	Score float32 `json:"score"`
}

var ErrClosed = errors.New("本地隐私推理引擎已关闭")
var ErrTokenLimit = errors.New("隐私检测文本超过 131072 token 上限")
var errModelIncompatible = errors.New("隐私模型格式、文件或算子与本地隐私引擎不兼容")

func validateSpans(ctx context.Context, text string, spans []Span) ([]Span, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted := make([]Span, 0, len(spans))
	for _, span := range spans {
		if span.Start < 0 || span.End <= span.Start || span.End > len(text) || !utf8.RuneStart(text[span.Start]) || (span.End < len(text) && !utf8.RuneStart(text[span.End])) || math.IsNaN(float64(span.Score)) || math.IsInf(float64(span.Score), 0) || span.Score < 0 || span.Score > 1 {
			return nil, errors.New("隐私模型返回无效片段")
		}
		if !canonicalKind(span.Label) {
			return nil, errors.New("隐私模型返回未知标签")
		}
		if span.Score >= MinimumScore {
			accepted = append(accepted, span)
		}
	}
	return accepted, nil
}
