package inference

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
)

// modelInput is one post-processed window of int64 tensors shaped [1, n].
type modelInput struct {
	ids, attention, typeIDs []int64
}

// runModel returns float32 logits shaped [1, n, labels] for one window.
type runModel func(ctx context.Context, input modelInput, labels int) ([]float32, error)

// detector holds everything except the ONNX session: tokenizer, sliding
// windows, stitching, decoding and span contracts.
type detector struct {
	tokenizer        *tokenizer
	decoder          *decoder
	spanContract     bool
	modelWindow      int
	contentWindow    int
	overlap          int
	maxRequestTokens int
	tokenTypeIDs     bool
	staticSequence   int
	padTokenID       int64
}

func readModelFile(directory, name string, limit int64) ([]byte, error) {
	file, err := os.Open(filepath.Join(directory, filepath.FromSlash(name)))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errInvalidManifest
	}
	return data, nil
}

func newDetector(directory string, m *modelManifest) (*detector, error) {
	tokenizerData, err := readModelFile(directory, m.tokenizerPath, 64<<20)
	if err != nil {
		return nil, err
	}
	t, err := loadTokenizer(tokenizerData)
	if err != nil {
		return nil, err
	}
	config, err := readModelFile(directory, m.configPath, 4<<20)
	if err != nil {
		return nil, err
	}
	offsetConsistency, err := configuredContract(config, decoderContractField, decoderContract, "invalid_decoder_contract")
	if err != nil {
		return nil, err
	}
	spanContract, err := configuredContract(config, spanContractField, spanContract, "invalid_span_contract")
	if err != nil {
		return nil, err
	}
	d, err := newHFDecoder(config, schemeBIO, m.labelMapping)
	if err != nil {
		return nil, err
	}
	if offsetConsistency {
		if err := d.withOffsetConsistency(); err != nil {
			return nil, err
		}
	}
	if spanContract {
		if _, err := loadSpanPatterns(); err != nil {
			return nil, err
		}
	}
	contentWindow := m.window - t.specialTokenCount()
	if contentWindow <= m.stride {
		return nil, errCode("invalid_model_window")
	}
	if m.staticSequence != 0 && (m.padTokenID >= int64(len(t.model.pieces))) {
		return nil, errInvalidManifest
	}
	return &detector{
		tokenizer: t, decoder: d, spanContract: spanContract,
		modelWindow: m.window, contentWindow: contentWindow, overlap: m.stride,
		maxRequestTokens: m.maxRequestTokens, tokenTypeIDs: m.tokenTypeIDs != "",
		staticSequence: m.staticSequence, padTokenID: m.padTokenID,
	}, nil
}

// chunkRanges covers count tokens with windows overlapping by overlap tokens.
func chunkRanges(count, window, overlap int) [][2]int {
	var ranges [][2]int
	for start := 0; start < count; {
		end := min(start+window, count)
		ranges = append(ranges, [2]int{start, end})
		if end == count {
			break
		}
		start = end - overlap
	}
	return ranges
}

// stitchedScores keeps, for each token, the log-softmax row from the window
// where the token sits farthest from a cut edge; ties keep the earlier one.
type stitchedScores struct {
	scores  []float64
	context []int // -1 until a window covers the token
	labels  int
}

func newStitchedScores(tokens, labels int) *stitchedScores {
	s := &stitchedScores{scores: make([]float64, tokens*labels), context: make([]int, tokens), labels: labels}
	for i := range s.context {
		s.context[i] = -1
	}
	return s
}

func (s *stitchedScores) addWindow(start int, logits []float32) error {
	if len(logits)%s.labels != 0 {
		return errCode("invalid_logits")
	}
	count := len(logits) / s.labels
	end := start + count
	if end > len(s.context) {
		return errCode("invalid_logits_shape")
	}
	for local := range count {
		row := logits[local*s.labels : (local+1)*s.labels]
		maximum := math.Inf(-1)
		for _, value := range row {
			if !finite32(value) {
				return errCode("invalid_logits")
			}
			maximum = math.Max(maximum, float64(value))
		}
		denominator := 0.0
		for _, value := range row {
			denominator += math.Exp(float64(value) - maximum)
		}
		if denominator == 0 || math.IsInf(denominator, 0) || math.IsNaN(denominator) {
			return errCode("invalid_logits")
		}
		normalizer := maximum + math.Log(denominator)
		// The text's own start and end hide no context.
		before, after := math.MaxInt, math.MaxInt
		if start != 0 {
			before = local
		}
		if end != len(s.context) {
			after = count - 1 - local
		}
		context := min(before, after)
		global := start + local
		if s.context[global] >= 0 && s.context[global] >= context {
			continue
		}
		for i, value := range row {
			s.scores[global*s.labels+i] = float64(value) - normalizer
		}
		s.context[global] = context
	}
	return nil
}

func (s *stitchedScores) finish() ([]float32, error) {
	if slices.Contains(s.context, -1) {
		return nil, errCode("incomplete_logits_coverage")
	}
	result := make([]float32, len(s.scores))
	for i, value := range s.scores {
		result[i] = float32(value)
	}
	return result, nil
}

func checkedTokenTotal(current, additional, maximum int) (int, error) {
	if additional > maximum-current {
		return 0, ErrTokenLimit
	}
	return current + additional, nil
}

// detect checks ctx before tokenizing and before every window.
func (d *detector) detect(ctx context.Context, text string, run runModel) (spans []Span, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			spans, err = nil, fmt.Errorf("%w: internal error", errInference)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoding, err := d.tokenizer.encode(text)
	if err != nil {
		return nil, err
	}
	if _, err := checkedTokenTotal(0, len(encoding.ids), d.maxRequestTokens); err != nil {
		return nil, err
	}
	if len(encoding.ids) == 0 {
		return []Span{}, nil
	}
	labels := d.decoder.labelCount()
	scores := newStitchedScores(len(encoding.ids), labels)
	for _, r := range chunkRanges(len(encoding.ids), d.contentWindow, d.overlap) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		w, err := d.tokenizer.prepareWindow(encoding, r[0], r[1])
		if err != nil {
			return nil, err
		}
		length := len(w.ids)
		if length > d.modelWindow {
			return nil, errCode("invalid_model_window")
		}
		input := modelInput{ids: w.ids, attention: w.attention}
		if d.tokenTypeIDs {
			input.typeIDs = w.typeIDs
		}
		if d.staticSequence != 0 {
			// Content positions index the unpadded prefix, so the padded
			// tail never reaches the stitched scores.
			if length > d.staticSequence {
				return nil, errCode("invalid_model_window")
			}
			input.ids = padded(input.ids, d.staticSequence, d.padTokenID)
			input.attention = padded(input.attention, d.staticSequence, 0)
			if input.typeIDs != nil {
				input.typeIDs = padded(input.typeIDs, d.staticSequence, 0)
			}
		}
		logits, err := run(ctx, input, labels)
		if err != nil {
			return nil, err
		}
		if len(logits) != len(input.ids)*labels {
			return nil, errCode("invalid_logits_shape")
		}
		content := make([]float32, 0, (r[1]-r[0])*labels)
		for _, position := range w.content {
			content = append(content, logits[position*labels:(position+1)*labels]...)
		}
		if err := scores.addWindow(r[0], content); err != nil {
			return nil, err
		}
	}
	stitched, err := scores.finish()
	if err != nil {
		return nil, err
	}
	spans, err = d.decoder.decode(stitched, encoding.offsets)
	if err != nil {
		return nil, err
	}
	if d.spanContract {
		if spans, err = spanContractRefine(text, spans); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(spans, func(a, b Span) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return a.End - b.End
	})
	return spans, nil
}

func padded(values []int64, length int, value int64) []int64 {
	result := make([]int64, length)
	copy(result, values)
	for i := len(values); i < length; i++ {
		result[i] = value
	}
	return result
}

var errInference = errors.New("本地隐私推理失败")
