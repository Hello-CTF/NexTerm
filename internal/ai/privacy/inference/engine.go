package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/gomlx/compute"
	"github.com/gomlx/compute/gobackend"
	"github.com/gomlx/gomlx/core/graph"
	"github.com/gomlx/gomlx/core/tensors"
	"github.com/gomlx/gomlx/ml/model"
	"github.com/gomlx/onnx-gomlx/onnx"
	"github.com/gomlx/onnx-gomlx/onnx/parser"
)

// Precision is the only model precision the engine runs. The INT8 export
// relies on ONNX Runtime's fused com.microsoft operators and the FP16 export
// is too slow without native half-precision kernels.
const Precision = "fp32"

// ErrUnsupportedPrecision rejects an installed INT8 or FP16 model, which
// earlier releases ran through ONNX Runtime.
var ErrUnsupportedPrecision = errors.New("本地隐私引擎只支持 FP32 模型，请下载 FP32 模型后选择它")

// threads bounds the compute workers. macOS gives goroutine threads the
// default QoS class, so they use at most half the cores and leave room for
// the user's interactive work.
func threads() int {
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		return max(1, min(runtime.NumCPU()/2, 4))
	}
	return min(runtime.NumCPU(), 4)
}

// sharedBackend is created once per process: a backend's worker pool is not
// stopped by Finalize, so a backend per engine would leak its goroutines
// every time the manager reopens a model.
var sharedBackend = sync.OnceValues(func() (compute.Backend, error) {
	return gobackend.New("parallelism=" + strconv.Itoa(threads()))
})

type Engine struct {
	detector *detector
	exec     *model.Exec
	gate     chan struct{}
}

// Open loads the installed model in modelDirectory. Model integrity is
// checked by the installer before Open.
func Open(modelDirectory, precision string) (*Engine, error) {
	if precision != Precision {
		return nil, ErrUnsupportedPrecision
	}
	modelDirectory, err := filepath.Abs(modelDirectory)
	if err != nil {
		return nil, err
	}
	manifest, err := os.Open(filepath.Join(modelDirectory, "installation.json"))
	if err != nil {
		return nil, errors.New("隐私模型安装信息不可用")
	}
	var installation struct {
		Precision string `json:"precision"`
	}
	err = json.NewDecoder(io.LimitReader(manifest, 2<<20)).Decode(&installation)
	manifest.Close()
	if err != nil || installation.Precision != precision {
		return nil, errors.New("隐私模型安装精度与请求不一致")
	}
	engine, err := openEngine(modelDirectory)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errModelIncompatible, err)
	}
	return engine, nil
}

func openEngine(directory string) (*Engine, error) {
	m, err := loadInstallationManifest(directory)
	if err != nil {
		return nil, err
	}
	if err := m.validateFiles(directory); err != nil {
		return nil, err
	}
	d, err := newDetector(directory, m)
	if err != nil {
		return nil, err
	}
	backend, err := sharedBackend()
	if err != nil {
		return nil, err
	}
	engine := &Engine{detector: d, gate: make(chan struct{}, 1)}
	if err := engine.compile(backend, filepath.Join(directory, filepath.FromSlash(m.modelPath)), m); err != nil {
		engine.release()
		return nil, err
	}
	engine.gate <- struct{}{}
	return engine, nil
}

// compile parses the ONNX graph, loads its weights and runs a two-token
// window, so a model with an unsupported operator or unexpected logits
// fails here instead of on the first request.
func (e *Engine) compile(backend compute.Backend, path string, m *modelManifest) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("model_compile_failed: %v", r)
		}
	}()
	parsed, err := parser.ParseFile(path)
	if err != nil {
		return err
	}
	if err := validateModelIO(parsed, m); err != nil {
		return err
	}
	store := model.NewStore()
	if err := parsed.VariablesToScope(store.RootScope()); err != nil {
		return err
	}
	inputs := []string{m.inputIDs, m.mask}
	if m.tokenTypeIDs != "" {
		inputs = append(inputs, m.tokenTypeIDs)
	}
	build := func(scope *model.Scope, values []*graph.Node) *graph.Node {
		g := values[0].Graph()
		scope.SetTraining(g, false)
		named := make(map[string]*graph.Node, len(inputs))
		for i, name := range inputs {
			named[name] = values[i]
		}
		return parsed.CallGraph(scope, g, named, m.outputName)[0]
	}
	e.exec, err = model.NewExec(backend, store, build)
	if err != nil {
		return err
	}
	axes := make([][]string, len(inputs))
	for i := range axes {
		axes[i] = []string{"batch", "sequence"}
	}
	e.exec.WithDynamicAxes(axes...)
	probe := modelInput{ids: []int64{0, 2}, attention: []int64{1, 1}}
	if m.tokenTypeIDs != "" {
		probe.typeIDs = []int64{0, 0}
	}
	_, err = e.window(probe, len(e.detector.decoder.labels))
	return err
}

// validateModelIO requires exactly the manifest inputs and the logits output.
func validateModelIO(parsed onnx.Model, m *modelManifest) error {
	actual, _ := parsed.Inputs()
	expected := []string{m.inputIDs, m.mask}
	if m.tokenTypeIDs != "" {
		expected = append(expected, m.tokenTypeIDs)
	}
	actual = slices.Clone(actual)
	slices.Sort(expected)
	slices.Sort(actual)
	outputs, _ := parsed.Outputs()
	if !slices.Equal(slices.Compact(actual), expected) || !slices.Contains(outputs, m.outputName) {
		return errCode("invalid_model_io")
	}
	return nil
}

// window runs one window and returns float32 logits shaped [1, n, labels].
func (e *Engine) window(input modelInput, labels int) (logits []float32, err error) {
	defer func() {
		if r := recover(); r != nil {
			logits, err = nil, errCode("model_run_failed")
		}
	}()
	length := len(input.ids)
	if length == 0 || len(input.attention) != length || (input.typeIDs != nil && len(input.typeIDs) != length) {
		return nil, errCode("invalid_model_window")
	}
	args := []any{tensors.FromFlatDataAndDimensions(input.ids, 1, length), tensors.FromFlatDataAndDimensions(input.attention, 1, length)}
	if input.typeIDs != nil {
		args = append(args, tensors.FromFlatDataAndDimensions(input.typeIDs, 1, length))
	}
	output, err := e.exec.Call1(args...)
	if err != nil {
		return nil, errCode("model_run_failed")
	}
	defer output.FinalizeAll()
	if !slices.Equal(output.Shape().Dimensions, []int{1, length, labels}) {
		return nil, errCode("invalid_logits_shape")
	}
	logits, err = tensors.CopyFlatData[float32](output)
	if err != nil {
		return nil, errCode("invalid_logits_shape")
	}
	return logits, nil
}

func (e *Engine) release() {
	if e.exec != nil {
		e.exec.Finalize()
		e.exec = nil
	}
}

// Detect is safe for concurrent use; calls run one at a time. Cancellation
// takes effect between windows.
func (e *Engine) Detect(ctx context.Context, text string) ([]Span, error) {
	if !utf8.ValidString(text) || len(text) > 16<<20 {
		return nil, errors.New("隐私检测文本无效或超过 16 MiB")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.gate:
	}
	defer func() { e.gate <- struct{}{} }()
	if e.exec == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if text == "" {
		return []Span{}, nil
	}
	spans, err := e.detector.detect(ctx, text, func(ctx context.Context, input modelInput, labels int) ([]float32, error) {
		logits, err := e.window(input, labels)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return logits, err
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		if errors.Is(err, ErrTokenLimit) || errors.Is(err, errInference) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", errInference, err)
	}
	return validateSpans(ctx, text, spans)
}

// Close waits for an in-flight Detect and releases the compiled model.
func (e *Engine) Close() error {
	<-e.gate
	defer func() { e.gate <- struct{}{} }()
	e.release()
	return nil
}
