package inference

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestChunksOverlapExactlyAndCoverAllTokens(t *testing.T) {
	if got := chunkRanges(8200, 4096, 128); !slices.Equal(got, [][2]int{{0, 4096}, {3968, 8064}, {7936, 8200}}) {
		t.Fatalf("got %v", got)
	}
	if got := chunkRanges(5, 2, 0); !slices.Equal(got, [][2]int{{0, 2}, {2, 4}, {4, 5}}) {
		t.Fatalf("got %v", got)
	}
}

func logSoftmax(row []float32) []float32 {
	maximum := math.Inf(-1)
	for _, v := range row {
		maximum = math.Max(maximum, float64(v))
	}
	sum := 0.0
	for _, v := range row {
		sum += math.Exp(float64(v) - maximum)
	}
	out := make([]float32, len(row))
	for i, v := range row {
		out[i] = float32(float64(v) - maximum - math.Log(sum))
	}
	return out
}

func TestStitchingKeepsWindowFarthestFromCutEdge(t *testing.T) {
	const labels = 33
	rows := func(preferred [][2]float32) []float32 {
		logits := make([]float32, len(preferred)*labels)
		for token, p := range preferred {
			logits[token*labels+int(p[0])] = p[1]
		}
		return logits
	}
	first := rows([][2]float32{{0, 0}, {0, 0}, {1, 2}, {1, 3}})
	second := rows([][2]float32{{2, 4}, {2, 5}, {0, 0}, {0, 0}})
	if !slices.Equal(chunkRanges(6, 4, 2), [][2]int{{0, 4}, {2, 6}}) {
		t.Fatal("ranges")
	}
	scores := newStitchedScores(6, labels)
	if err := scores.addWindow(0, first); err != nil {
		t.Fatal(err)
	}
	if err := scores.addWindow(2, second); err != nil {
		t.Fatal(err)
	}
	stitched, err := scores.finish()
	if err != nil {
		t.Fatal(err)
	}
	check := func(token int, want []float32) {
		for i, v := range stitched[token*labels : (token+1)*labels] {
			if math.Abs(float64(v-want[i])) > 1e-6 {
				t.Fatalf("token %d label %d: %v != %v", token, i, v, want[i])
			}
		}
	}
	check(2, logSoftmax(first[2*labels:3*labels]))
	check(3, logSoftmax(second[labels:2*labels]))
	if _, err := newStitchedScores(2, 3).finish(); err == nil {
		t.Fatal("incomplete coverage accepted")
	}
}

func TestValueCutAtWindowStartKeepsFullSpan(t *testing.T) {
	d, err := newHFDecoder(labelConfig(t, []string{"O", "B-account", "I-account"}, nil), schemeBIO, map[string]*string{"account": ptr("account")})
	if err != nil {
		t.Fatal(err)
	}
	const labels, tokens = 3, 8
	offsets := make([][2]int, tokens)
	for i := range offsets {
		offsets[i] = [2]int{i, i + 1}
	}
	ranges := chunkRanges(tokens, 6, 4)
	scores := newStitchedScores(tokens, labels)
	for _, r := range ranges {
		logits := filled((r[1]-r[0])*labels, -20)
		for global := r[0]; global < r[1]; global++ {
			row := logits[(global-r[0])*labels:]
			switch {
			case r[0] == 0 && global == 1:
				row[1] = 2
			case r[0] == 0 && (global == 2 || global == 3):
				row[2] = 2
			default:
				row[0] = 5
			}
		}
		if err := scores.addWindow(r[0], logits); err != nil {
			t.Fatal(err)
		}
	}
	stitched, err := scores.finish()
	if err != nil {
		t.Fatal(err)
	}
	spans, err := d.decode(stitched, offsets)
	if err != nil || len(spans) != 1 || spans[0].Start != 1 || spans[0].End != 4 {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestTokenLimitIsEnforced(t *testing.T) {
	if total, err := checkedTokenTotal(maxRequestTokens-1, 1, maxRequestTokens); err != nil || total != maxRequestTokens {
		t.Fatalf("boundary: %d %v", total, err)
	}
	if _, err := checkedTokenTotal(maxRequestTokens, 1, maxRequestTokens); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("over limit: %v", err)
	}
}

func syntheticDetector(t *testing.T) *detector {
	t.Helper()
	d, err := newHFDecoder(labelConfig(t, guardLabels(), nil), schemeBIO, guardMapping())
	if err != nil {
		t.Fatal(err)
	}
	return &detector{tokenizer: loadSynthetic(t), decoder: d, modelWindow: 4, contentWindow: 2, overlap: 1, maxRequestTokens: maxRequestTokens, tokenTypeIDs: true}
}

// secretModel marks every "▁secret" token (id 5) as a common_secret.
func secretModel(_ context.Context, input modelInput, labels int) ([]float32, error) {
	logits := make([]float32, len(input.ids)*labels)
	for position, id := range input.ids {
		label := 0
		if id == 5 {
			label = 13
		}
		logits[position*labels+label] = 10
	}
	return logits, nil
}

func TestDetectorStitchesWindowsIntoUTF8Spans(t *testing.T) {
	text := "你好 secret 你好 secret"
	spans, err := syntheticDetector(t).detect(t.Context(), text, func(ctx context.Context, input modelInput, labels int) ([]float32, error) {
		if len(input.ids) != 4 || input.ids[0] != 0 || input.ids[3] != 2 || len(input.typeIDs) != 4 {
			t.Fatalf("window %+v", input)
		}
		return secretModel(ctx, input, labels)
	})
	if err != nil || len(spans) != 2 || text[spans[0].Start:spans[0].End] != "secret" || text[spans[1].Start:spans[1].End] != "secret" || spans[1].Start != 21 || spans[0].Label != "common_secret" {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestDetectorStaticPaddingKeepsContentPositions(t *testing.T) {
	d := syntheticDetector(t)
	d.staticSequence, d.padTokenID = 4, 1
	d.contentWindow = 1
	d.overlap = 0
	spans, err := d.detect(t.Context(), "secret", func(ctx context.Context, input modelInput, labels int) ([]float32, error) {
		if !slices.Equal(input.ids, []int64{0, 5, 2, 1}) || !slices.Equal(input.attention, []int64{1, 1, 1, 0}) || !slices.Equal(input.typeIDs, []int64{0, 0, 0, 0}) {
			t.Fatalf("padded window %+v", input)
		}
		return secretModel(ctx, input, labels)
	})
	if err != nil || len(spans) != 1 || spans[0].Start != 0 || spans[0].End != 6 {
		t.Fatalf("got %+v %v", spans, err)
	}
}

func TestDetectorHonoursCancellationLimitsAndFailures(t *testing.T) {
	d := syntheticDetector(t)
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := d.detect(ctx, "你好 secret 你好 secret", func(ctx context.Context, input modelInput, labels int) ([]float32, error) {
		calls++
		cancel()
		return secretModel(ctx, input, labels)
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancel between windows: %v after %d calls", err, calls)
	}
	d.maxRequestTokens = 3
	if _, err := d.detect(t.Context(), "你好 secret 你好 secret", secretModel); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("token limit: %v", err)
	}
	d.maxRequestTokens = maxRequestTokens
	if _, err := d.detect(t.Context(), "secret", func(context.Context, modelInput, int) ([]float32, error) { panic("boom") }); !errors.Is(err, errInference) {
		t.Fatalf("panic: %v", err)
	}
	if _, err := d.detect(t.Context(), "secret", func(context.Context, modelInput, int) ([]float32, error) { return []float32{1}, nil }); err == nil {
		t.Fatal("bad logits shape accepted")
	}
	if _, err := d.detect(t.Context(), "secret", func(_ context.Context, input modelInput, labels int) ([]float32, error) {
		return filled(len(input.ids)*labels, float32(math.Inf(1))), nil
	}); err == nil {
		t.Fatal("non-finite logits accepted")
	}
}

func writeInstallation(t *testing.T, precision string, mutate func(map[string]any)) string {
	t.Helper()
	directory := t.TempDir()
	model := map[string]string{"int8": "model_int8.onnx", "fp32": "model.onnx", "fp16": "model_fp16_static512.onnx"}[precision]
	names := []string{"config.json", "tokenizer.json", "labels.json", model}
	if precision != "fp16" {
		names = append(names, model+"_data")
	}
	files := map[string]any{}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		files[name] = map[string]any{"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "size": 1}
	}
	installation := map[string]any{"version": "v0.1.3", "revision": "43a10bc91773516fa68266b68d295a3011642022", "precision": precision, "files": files}
	if mutate != nil {
		mutate(installation)
	}
	data, err := json.Marshal(installation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "installation.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestInstallationManifestAdapter(t *testing.T) {
	for _, precision := range []string{"int8", "fp32", "fp16"} {
		directory := writeInstallation(t, precision, nil)
		m, err := loadInstallationManifest(directory)
		if err != nil {
			t.Fatalf("%s: %v", precision, err)
		}
		if err := m.validateFiles(directory); err != nil {
			t.Fatalf("%s files: %v", precision, err)
		}
		if (precision == "fp16") != (m.staticSequence == 512 && m.padTokenID == 1 && len(m.externalDataPaths) == 0) {
			t.Fatalf("%s execution: %+v", precision, m)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown field": func(m map[string]any) { m["extra"] = true },
		"empty version": func(m map[string]any) { m["version"] = "" },
		"bad revision":  func(m map[string]any) { m["revision"] = "HEAD" },
		"bad precision": func(m map[string]any) { m["precision"] = "q4" },
		"missing model": func(m map[string]any) { delete(m["files"].(map[string]any), "model_int8.onnx") },
		"parent path": func(m map[string]any) {
			m["files"].(map[string]any)["../x"] = map[string]any{"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "size": 1}
		},
		"zero size": func(m map[string]any) {
			m["files"].(map[string]any)["config.json"] = map[string]any{"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "size": 0}
		},
		"file unknown key": func(m map[string]any) { m["files"].(map[string]any)["config.json"].(map[string]any)["url"] = "x" },
		"uppercase sha256": func(m map[string]any) {
			m["files"].(map[string]any)["config.json"].(map[string]any)["sha256"] = "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"
		},
	} {
		if _, err := loadInstallationManifest(writeInstallation(t, "int8", mutate)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	directory := writeInstallation(t, "int8", nil)
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte("longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadInstallationManifest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.validateFiles(directory); err == nil {
		t.Fatal("size mismatch accepted")
	}
}

func TestRelativePathsAndKinds(t *testing.T) {
	for _, path := range []string{"../model.onnx", "onnx//model.onnx", "C:model.onnx", "/abs", "dir/", `a\b`, "./x", ""} {
		if validRelativePath(path) {
			t.Errorf("%q accepted", path)
		}
	}
	if !validRelativePath("int8/model.onnx") || canonicalKind("private_email") || !canonicalKind("email") {
		t.Fatal("valid values rejected")
	}
}
