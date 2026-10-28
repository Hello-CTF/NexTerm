//go:build cgo

package inference

/*
#cgo linux LDFLAGS: -ldl
#include <stdlib.h>
#include "ort_shim.h"
*/
import "C"

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
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

var onnxRuntime = struct {
	sync.Mutex
	path string
}{}

func selfLibraryPath() string {
	path := C.ng_self_path()
	if path == nil {
		return ""
	}
	return C.GoString(path)
}

// initializeRuntime loads ONNX Runtime once per process. A process cannot
// switch to a different runtime library after the first one is loaded.
func initializeRuntime(path string) error {
	onnxRuntime.Lock()
	defer onnxRuntime.Unlock()
	if onnxRuntime.path != "" {
		if onnxRuntime.path != path {
			return ErrUnavailable
		}
		return nil
	}
	value := C.CString(path)
	defer C.free(unsafe.Pointer(value))
	var detail [512]C.char
	if C.ng_ort_init(value, &detail[0], C.size_t(len(detail))) != C.NG_OK {
		if message := strings.TrimSpace(C.GoString(&detail[0])); message != "" {
			return fmt.Errorf("%w: %s", ErrUnavailable, message)
		}
		return ErrUnavailable
	}
	onnxRuntime.path = path
	return nil
}

// runtimeVersion reports the loaded ONNX Runtime version, or "" before the
// runtime is initialized.
func runtimeVersion() string {
	version := C.ng_ort_version()
	if version == nil {
		return ""
	}
	return C.GoString(version)
}

type Engine struct {
	detector *detector
	session  unsafe.Pointer
	names    []*C.char // input_ids, attention_mask, token_type_ids or nil, logits
	gate     chan struct{}
}

// Open loads the selected model. Runtime libraries must be application-owned,
// not user-supplied model files. Model integrity is checked by the installer.
func Open(modelDirectory, runtimeDirectory, precision string) (*Engine, error) {
	if err := CheckPlatform(); err != nil {
		return nil, err
	}
	if precision != "int8" && precision != "fp32" && precision != "fp16" {
		return nil, errors.New("未知的隐私模型精度")
	}
	if strings.ContainsRune(modelDirectory, 0) || strings.ContainsRune(runtimeDirectory, 0) {
		return nil, ErrUnavailable
	}
	onnxPath, err := runtimePath(runtimeDirectory)
	if err != nil {
		return nil, err
	}
	modelDirectory, err = filepath.Abs(modelDirectory)
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
	if err := initializeRuntime(onnxPath); err != nil {
		return nil, err
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
	path := C.CString(filepath.Join(directory, filepath.FromSlash(m.modelPath)))
	defer C.free(unsafe.Pointer(path))
	engine := &Engine{detector: d, gate: make(chan struct{}, 1)}
	if C.ng_ort_session_open(path, C.int(min(runtime.NumCPU(), 4)), &engine.session) != C.NG_OK {
		return nil, errCode("model_session_failed")
	}
	engine.names = []*C.char{C.CString(m.inputIDs), C.CString(m.mask), nil, C.CString(m.outputName)}
	if m.tokenTypeIDs != "" {
		engine.names[2] = C.CString(m.tokenTypeIDs)
	}
	if err := engine.validateSession(m); err != nil {
		engine.release()
		return nil, err
	}
	engine.gate <- struct{}{}
	return engine, nil
}

type tensorInfo struct {
	name        string
	elementType int
	dims        []int64
}

func (e *Engine) sessionIO(output bool) ([]tensorInfo, error) {
	kind := C.int(0)
	if output {
		kind = 1
	}
	var count C.size_t
	if C.ng_ort_session_io_count(e.session, kind, &count) != C.NG_OK || count > 64 {
		return nil, errCode("invalid_model_io")
	}
	infos := make([]tensorInfo, 0, int(count))
	name := (*C.char)(C.malloc(256))
	defer C.free(unsafe.Pointer(name))
	var dims [8]C.int64_t
	for i := range int(count) {
		var elementType C.int
		var dimsCount C.size_t
		if C.ng_ort_session_io(e.session, kind, C.size_t(i), name, 256, &elementType, &dims[0], C.size_t(len(dims)), &dimsCount) != C.NG_OK {
			return nil, errCode("invalid_model_io")
		}
		info := tensorInfo{name: C.GoString(name), elementType: int(elementType)}
		for _, d := range dims[:dimsCount] {
			info.dims = append(info.dims, int64(d))
		}
		infos = append(infos, info)
	}
	return infos, nil
}

const (
	elementFloat = 1
	elementInt64 = 7
)

// validateSession requires exactly the manifest inputs and the logits output.
// A static FP16 model must declare int64 [1, length] inputs and float32
// logits, so a dynamic graph is never padded.
func (e *Engine) validateSession(m *modelManifest) error {
	inputs, err := e.sessionIO(false)
	if err != nil {
		return err
	}
	outputs, err := e.sessionIO(true)
	if err != nil {
		return err
	}
	expected := []string{m.inputIDs, m.mask}
	if m.tokenTypeIDs != "" {
		expected = append(expected, m.tokenTypeIDs)
	}
	actual := make([]string, 0, len(inputs))
	for _, input := range inputs {
		actual = append(actual, input.name)
	}
	slices.Sort(expected)
	slices.Sort(actual)
	actual = slices.Compact(actual)
	output := slices.IndexFunc(outputs, func(o tensorInfo) bool { return o.name == m.outputName })
	if !slices.Equal(actual, expected) || output < 0 {
		return errCode("invalid_model_io")
	}
	if m.staticSequence != 0 {
		for _, input := range inputs {
			if input.elementType != elementInt64 || !slices.Equal(input.dims, []int64{1, int64(m.staticSequence)}) {
				return errCode("invalid_model_io")
			}
		}
		for _, o := range outputs {
			if o.name == m.outputName && o.elementType != elementFloat {
				return errCode("invalid_model_io")
			}
		}
	}
	return nil
}

func (e *Engine) release() {
	if e.session != nil {
		C.ng_ort_session_close(e.session)
		e.session = nil
	}
	for _, name := range e.names {
		if name != nil {
			C.free(unsafe.Pointer(name))
		}
	}
	e.names = nil
}

// Detect is safe for concurrent use; calls run one at a time.
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
	if e.session == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if text == "" {
		return []Span{}, nil
	}
	options := C.ng_ort_run_options_new()
	if options == nil {
		return nil, fmt.Errorf("%w: run_options", errInference)
	}
	finished, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			C.ng_ort_run_options_terminate(options)
		case <-finished:
		}
	}()
	spans, err := e.detector.detect(ctx, text, e.run(options))
	close(finished)
	<-stopped
	C.ng_ort_run_options_free(options)
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

func (e *Engine) run(options unsafe.Pointer) runModel {
	return func(ctx context.Context, input modelInput, labels int) ([]float32, error) {
		length := len(input.ids)
		if length == 0 || len(input.attention) != length || (input.typeIDs != nil && len(input.typeIDs) != length) || (input.typeIDs != nil) != (e.names[2] != nil) {
			return nil, errCode("invalid_model_window")
		}
		output := make([]float32, length*labels)
		var types *C.int64_t
		if input.typeIDs != nil {
			types = (*C.int64_t)(unsafe.Pointer(&input.typeIDs[0]))
		}
		code := C.ng_ort_run(e.session, options, e.names[0], e.names[1], e.names[2],
			(*C.int64_t)(unsafe.Pointer(&input.ids[0])), (*C.int64_t)(unsafe.Pointer(&input.attention[0])), types,
			C.int64_t(length), e.names[3], (*C.float)(unsafe.Pointer(&output[0])), C.int64_t(labels))
		runtime.KeepAlive(input)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch code {
		case C.NG_OK:
			return output, nil
		case C.NG_SHAPE:
			return nil, errCode("invalid_logits_shape")
		default:
			return nil, errCode("model_run_failed")
		}
	}
}

// Close waits for an in-flight Detect and releases the session.
func (e *Engine) Close() error {
	<-e.gate
	defer func() { e.gate <- struct{}{} }()
	e.release()
	return nil
}
