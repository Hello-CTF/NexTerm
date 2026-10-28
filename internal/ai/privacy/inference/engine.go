//go:build (linux || darwin || windows) && (amd64 || arm64) && (cgo || !(android || ios))

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
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/ebitengine/purego"
)

// onnxRuntime holds the process-wide ONNX Runtime state. The library stays
// loaded for the process lifetime once initialized: ONNX Runtime owns
// threads and a global environment that must never outlive its code.
var onnxRuntime = struct {
	sync.Mutex
	path   string
	base   unsafe.Pointer // const OrtApiBase *
	api    unsafe.Pointer // const OrtApi *
	env    uintptr        // OrtEnv *
	memory uintptr        // OrtMemoryInfo *
}{}

const pointerSize = int(unsafe.Sizeof(uintptr(0)))

// ortFunction returns the OrtApi function pointer in slot.
func ortFunction(slot int) uintptr {
	return *(*uintptr)(unsafe.Add(onnxRuntime.api, slot*pointerSize))
}

// ortCall calls an OrtApi function. Every argument is an integer, enum or
// pointer, so the C ABI matches SyscallN. Go memory passed as
// uintptr(unsafe.Pointer(p)) in the call stays on the heap and alive for the
// call.
//
//go:uintptrescapes
func ortCall(slot int, args ...uintptr) uintptr {
	result, _, _ := purego.SyscallN(ortFunction(slot), args...)
	return result
}

// ortFailed releases a non-nil OrtStatus and reports the failure; status
// messages never cross into Go.
func ortFailed(status uintptr) bool {
	if status == 0 {
		return false
	}
	ortCall(slotReleaseStatus, status)
	return true
}

// pointer converts a C pointer returned as an integer.
func pointer(value uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&value)) }

// cString copies a NUL-terminated C string shorter than limit bytes; ok is
// false for a nil or longer string.
func cString(value unsafe.Pointer, limit int) (string, bool) {
	if value == nil {
		return "", false
	}
	for i := range limit {
		if *(*byte)(unsafe.Add(value, i)) == 0 {
			return string(unsafe.Slice((*byte)(value), i)), true
		}
	}
	return "", false
}

func nulTerminated(value string) []byte { return append([]byte(value), 0) }

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
	if err := loadRuntime(path); err != nil {
		if message := strings.TrimSpace(err.Error()); message != "" {
			return &RuntimeError{Path: path, Err: errors.New(message)}
		}
		return &RuntimeError{Path: path, Err: ErrUnavailable}
	}
	onnxRuntime.path = path
	return nil
}

// loadRuntime resolves OrtGetApiBase, requests C API version 23 and creates
// the environment and CPU memory info. A library that fails initialization
// is unloaded. Errors carry a loader or version diagnostic, never user text.
func loadRuntime(path string) error {
	library, err := loadLibrary(path)
	if err != nil {
		return err
	}
	getBase, err := librarySymbol(library, "OrtGetApiBase")
	if err != nil {
		unloadLibrary(library)
		return err
	}
	base, _, _ := purego.SyscallN(getBase)
	// OrtApiBase is {GetApi, GetVersionString}.
	var getAPI, getVersion uintptr
	if base != 0 {
		getAPI = *(*uintptr)(pointer(base))
		getVersion = *(*uintptr)(unsafe.Add(pointer(base), pointerSize))
	}
	if getAPI == 0 || getVersion == 0 {
		unloadLibrary(library)
		return errors.New("OrtGetApiBase returned no API base")
	}
	api, _, _ := purego.SyscallN(getAPI, ortAPIVersion)
	if api == 0 {
		version, _ := cString(pointer(callVersion(getVersion)), 256)
		if version == "" {
			version = "unknown"
		}
		unloadLibrary(library)
		return fmt.Errorf("ONNX Runtime %s does not provide C API version %d", version, ortAPIVersion)
	}
	onnxRuntime.api = pointer(api)
	// The default logger writes to stderr; the FATAL level keeps ONNX Runtime
	// output out of the application without a C logging callback.
	logID := nulTerminated("nexterm-privacy")
	var env uintptr
	if ortFailed(ortCall(slotCreateEnv, ortLoggingLevelFatal, uintptr(unsafe.Pointer(&logID[0])), uintptr(unsafe.Pointer(&env)))) || env == 0 {
		onnxRuntime.api = nil
		unloadLibrary(library)
		return errors.New("ONNX Runtime environment creation failed")
	}
	ortFailed(ortCall(slotDisableTelemetryEvents, env))
	var memory uintptr
	if ortFailed(ortCall(slotCreateCPUMemoryInfo, ortArenaAllocator, ortMemTypeDefault, uintptr(unsafe.Pointer(&memory)))) || memory == 0 {
		ortCall(slotReleaseEnv, env)
		onnxRuntime.api = nil
		unloadLibrary(library)
		return errors.New("ONNX Runtime CPU memory info creation failed")
	}
	onnxRuntime.base = pointer(base)
	onnxRuntime.env = env
	onnxRuntime.memory = memory
	return nil
}

func callVersion(getVersion uintptr) uintptr {
	version, _, _ := purego.SyscallN(getVersion)
	return version
}

// runtimeVersion reports the loaded ONNX Runtime version, or "" before the
// runtime is initialized.
func runtimeVersion() string {
	onnxRuntime.Lock()
	defer onnxRuntime.Unlock()
	if onnxRuntime.base == nil {
		return ""
	}
	version, _ := cString(pointer(callVersion(*(*uintptr)(unsafe.Add(onnxRuntime.base, pointerSize)))), 256)
	return version
}

// intraOpThreads bounds the intra-op pool. macOS starts pool threads at the
// default QoS class, so they use at most half the cores and leave room for
// the user's interactive work.
func intraOpThreads() int {
	if runtime.GOOS == "darwin" {
		return max(1, min(runtime.NumCPU()/2, 4))
	}
	return min(runtime.NumCPU(), 4)
}

type Engine struct {
	detector *detector
	session  uintptr  // OrtSession *
	names    [][]byte // NUL-terminated input_ids, attention_mask, token_type_ids or nil, logits
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
	engine := &Engine{detector: d, gate: make(chan struct{}, 1)}
	engine.session = openSession(filepath.Join(directory, filepath.FromSlash(m.modelPath)), intraOpThreads())
	if engine.session == 0 {
		return nil, errCode("model_session_failed")
	}
	engine.names = [][]byte{nulTerminated(m.inputIDs), nulTerminated(m.mask), nil, nulTerminated(m.outputName)}
	if m.tokenTypeIDs != "" {
		engine.names[2] = nulTerminated(m.tokenTypeIDs)
	}
	if err := engine.validateSession(m); err != nil {
		engine.release()
		return nil, err
	}
	engine.gate <- struct{}{}
	return engine, nil
}

// openSession creates a CPU session for modelPath, or returns 0.
func openSession(modelPath string, threads int) uintptr {
	path, err := nativePath(modelPath)
	if err != nil {
		return 0
	}
	var options uintptr
	if ortFailed(ortCall(slotCreateSessionOptions, uintptr(unsafe.Pointer(&options)))) || options == 0 {
		return 0
	}
	spinning, disabled := nulTerminated("session.intra_op.allow_spinning"), nulTerminated("0")
	failed := ortFailed(ortCall(slotSetSessionLogSeverityLevel, options, ortLoggingLevelFatal))
	failed = ortFailed(ortCall(slotSetIntraOpNumThreads, options, uintptr(threads))) || failed
	// Without spinning, idle pool threads sleep between operators instead of
	// holding their cores while Go tokenizes and decodes.
	failed = ortFailed(ortCall(slotAddSessionConfigEntry, options, uintptr(unsafe.Pointer(&spinning[0])), uintptr(unsafe.Pointer(&disabled[0])))) || failed
	failed = ortFailed(ortCall(slotSetInterOpNumThreads, options, 1)) || failed
	failed = ortFailed(ortCall(slotSetSessionExecutionMode, options, ortSequential)) || failed
	failed = ortFailed(ortCall(slotSetSessionGraphOptimization, options, ortEnableLayout)) || failed
	var session uintptr
	if !failed {
		failed = ortFailed(ortCall(slotCreateSession, onnxRuntime.env, uintptr(path), options, uintptr(unsafe.Pointer(&session))))
	}
	runtime.KeepAlive(path)
	ortCall(slotReleaseSessionOptions, options)
	if failed {
		return 0
	}
	return session
}

type tensorInfo struct {
	name        string
	elementType int
	dims        []int64
}

func (e *Engine) sessionIO(output bool) ([]tensorInfo, error) {
	countSlot, nameSlot, typeSlot := slotSessionGetInputCount, slotSessionGetInputName, slotSessionGetInputTypeInfo
	if output {
		countSlot, nameSlot, typeSlot = slotSessionGetOutputCount, slotSessionGetOutputName, slotSessionGetOutputTypeInfo
	}
	var count uintptr
	if ortFailed(ortCall(countSlot, e.session, uintptr(unsafe.Pointer(&count)))) || count > 64 {
		return nil, errCode("invalid_model_io")
	}
	infos := make([]tensorInfo, 0, int(count))
	for i := range count {
		info, ok := e.tensorInfo(nameSlot, typeSlot, i)
		if !ok {
			return nil, errCode("invalid_model_io")
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// tensorInfo reads one input or output name, element type and shape,
// freeing the allocator-owned name and the type info on every path.
func (e *Engine) tensorInfo(nameSlot, typeSlot int, index uintptr) (tensorInfo, bool) {
	info := tensorInfo{elementType: -1}
	var allocator uintptr
	if ortFailed(ortCall(slotGetAllocatorWithDefaultOptions, uintptr(unsafe.Pointer(&allocator)))) {
		return info, false
	}
	var value unsafe.Pointer
	if ortFailed(ortCall(nameSlot, e.session, index, allocator, uintptr(unsafe.Pointer(&value)))) || value == nil {
		return info, false
	}
	name, fits := cString(value, 256)
	ortFailed(ortCall(slotAllocatorFree, allocator, uintptr(value)))
	if !fits {
		return info, false
	}
	info.name = name
	var typeInfo uintptr
	if ortFailed(ortCall(typeSlot, e.session, index, uintptr(unsafe.Pointer(&typeInfo)))) {
		return info, false
	}
	defer ortCall(slotReleaseTypeInfo, typeInfo)
	var tensor uintptr // owned by typeInfo
	if ortFailed(ortCall(slotCastTypeInfoToTensorInfo, typeInfo, uintptr(unsafe.Pointer(&tensor)))) {
		return info, false
	}
	if tensor == 0 {
		return info, true
	}
	var elementType int32
	var dimsCount uintptr
	var dims [8]int64
	if ortFailed(ortCall(slotGetTensorElementType, tensor, uintptr(unsafe.Pointer(&elementType)))) ||
		ortFailed(ortCall(slotGetDimensionsCount, tensor, uintptr(unsafe.Pointer(&dimsCount)))) ||
		dimsCount > uintptr(len(dims)) ||
		ortFailed(ortCall(slotGetDimensions, tensor, uintptr(unsafe.Pointer(&dims[0])), dimsCount)) {
		return info, false
	}
	info.elementType = int(elementType)
	info.dims = slices.Clone(dims[:dimsCount])
	return info, true
}

const (
	elementFloat = ortTensorFloat
	elementInt64 = ortTensorInt64
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
	if e.session != 0 {
		ortCall(slotReleaseSession, e.session)
		e.session = 0
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
	if e.session == 0 {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if text == "" {
		return []Span{}, nil
	}
	var options uintptr
	if ortFailed(ortCall(slotCreateRunOptions, uintptr(unsafe.Pointer(&options)))) || options == 0 {
		return nil, fmt.Errorf("%w: run_options", errInference)
	}
	finished, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			ortFailed(ortCall(slotRunOptionsSetTerminate, options))
		case <-finished:
		}
	}()
	spans, err := e.detector.detect(ctx, text, e.run(options))
	close(finished)
	<-stopped
	ortCall(slotReleaseRunOptions, options)
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

func (e *Engine) run(options uintptr) runModel {
	return func(ctx context.Context, input modelInput, labels int) ([]float32, error) {
		length := len(input.ids)
		if length == 0 || len(input.attention) != length || (input.typeIDs != nil && len(input.typeIDs) != length) || (input.typeIDs != nil) != (e.names[2] != nil) {
			return nil, errCode("invalid_model_window")
		}
		output := make([]float32, length*labels)
		ok, shaped := e.runWindow(options, input, output, labels)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case ok:
			return output, nil
		case shaped:
			return nil, errCode("invalid_logits_shape")
		default:
			return nil, errCode("model_run_failed")
		}
	}
}

// runWindow runs one window whose tensors wrap Go memory pinned for the
// duration of the call, and copies float32 logits shaped [1, length, labels]
// into output. shaped reports a run that produced logits of another shape or
// type. Every OrtValue is released on every path.
func (e *Engine) runWindow(options uintptr, input modelInput, output []float32, labels int) (ok, shaped bool) {
	length := len(input.ids)
	var pinner runtime.Pinner
	defer pinner.Unpin()
	tensor := func(data []int64) uintptr {
		pinner.Pin(&data[0])
		shape := [2]int64{1, int64(length)}
		var value uintptr
		if ortFailed(ortCall(slotCreateTensorWithDataAsOrtValue, onnxRuntime.memory, uintptr(unsafe.Pointer(&data[0])), uintptr(length*8),
			uintptr(unsafe.Pointer(&shape[0])), 2, ortTensorInt64, uintptr(unsafe.Pointer(&value)))) {
			return 0
		}
		return value
	}
	var inputs [3]uintptr
	defer func() {
		for _, value := range inputs {
			if value != 0 {
				ortCall(slotReleaseValue, value)
			}
		}
	}()
	inputs[0], inputs[1] = tensor(input.ids), tensor(input.attention)
	count := uintptr(2)
	if input.typeIDs != nil {
		inputs[2], count = tensor(input.typeIDs), 3
	}
	if inputs[0] == 0 || inputs[1] == 0 || (count == 3 && inputs[2] == 0) {
		return false, false
	}
	names := [3]uintptr{uintptr(unsafe.Pointer(&e.names[0][0])), uintptr(unsafe.Pointer(&e.names[1][0]))}
	if count == 3 {
		names[2] = uintptr(unsafe.Pointer(&e.names[2][0]))
	}
	outputNames := [1]uintptr{uintptr(unsafe.Pointer(&e.names[3][0]))}
	var result uintptr
	failed := ortFailed(ortCall(slotRun, e.session, options, uintptr(unsafe.Pointer(&names[0])), uintptr(unsafe.Pointer(&inputs[0])), count,
		uintptr(unsafe.Pointer(&outputNames[0])), 1, uintptr(unsafe.Pointer(&result))))
	runtime.KeepAlive(e.names)
	if result != 0 {
		defer ortCall(slotReleaseValue, result)
	}
	if failed || result == 0 {
		return false, false
	}
	var info uintptr
	if ortFailed(ortCall(slotGetTensorTypeAndShape, result, uintptr(unsafe.Pointer(&info)))) {
		return false, true
	}
	defer ortCall(slotReleaseTensorTypeAndShapeInfo, info)
	var elementType int32
	var dimsCount uintptr
	var dims [3]int64
	var data unsafe.Pointer
	if ortFailed(ortCall(slotGetTensorElementType, info, uintptr(unsafe.Pointer(&elementType)))) || elementType != ortTensorFloat ||
		ortFailed(ortCall(slotGetDimensionsCount, info, uintptr(unsafe.Pointer(&dimsCount)))) || dimsCount != 3 ||
		ortFailed(ortCall(slotGetDimensions, info, uintptr(unsafe.Pointer(&dims[0])), 3)) ||
		dims != [3]int64{1, int64(length), int64(labels)} ||
		ortFailed(ortCall(slotGetTensorMutableData, result, uintptr(unsafe.Pointer(&data)))) || data == nil {
		return false, true
	}
	copy(output, unsafe.Slice((*float32)(data), length*labels))
	return true, false
}

// Close waits for an in-flight Detect and releases the session.
func (e *Engine) Close() error {
	<-e.gate
	defer func() { e.gate <- struct{}{} }()
	e.release()
	return nil
}
