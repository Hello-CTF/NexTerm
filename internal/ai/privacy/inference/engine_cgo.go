//go:build cgo

package inference

/*
#cgo linux LDFLAGS: -ldl
#include "bridge.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

var libraries = struct {
	sync.Mutex
	loaded   map[string]*C.ng_api
	runtimes map[string]bool
}{loaded: make(map[string]*C.ng_api), runtimes: make(map[string]bool)}

func selfLibraryPath() string {
	path := C.ng_self_path()
	if path == nil {
		return ""
	}
	return C.GoString(path)
}

type Engine struct {
	api    *C.ng_api
	handle unsafe.Pointer
	gate   chan struct{}
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
	enginePath, onnxPath, err := runtimePaths(runtimeDirectory)
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
	libraries.Lock()
	// Windows must resolve the ORT DLL's bundled VC runtime beside the DLL,
	// rather than searching the application executable directory or PATH.
	if runtime.GOOS == "windows" && !libraries.runtimes[onnxPath] {
		path := C.CString(onnxPath)
		loaded := C.ng_preload_runtime(path) != 0
		C.free(unsafe.Pointer(path))
		if !loaded {
			libraries.Unlock()
			return nil, ErrUnavailable
		}
		libraries.runtimes[onnxPath] = true
	}
	api := libraries.loaded[enginePath]
	if api == nil {
		path := C.CString(enginePath)
		api = C.ng_load(path)
		C.free(unsafe.Pointer(path))
		if api != nil {
			libraries.loaded[enginePath] = api
		}
	}
	libraries.Unlock()
	if api == nil {
		return nil, ErrUnavailable
	}
	directory, path := C.CString(modelDirectory), C.CString(onnxPath)
	defer C.free(unsafe.Pointer(directory))
	defer C.free(unsafe.Pointer(path))
	engine := &Engine{api: api, gate: make(chan struct{}, 1)}
	if err := nativeError(int(C.ng_open(api, directory, path, C.uint32_t(min(runtime.NumCPU(), 4)), &engine.handle))); err != nil {
		return nil, err
	}
	engine.gate <- struct{}{}
	return engine, nil
}

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
	if e.handle == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if text == "" {
		return []Span{}, nil
	}
	C.ng_reset(e.api, e.handle)
	finished, cancelled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(cancelled)
		select {
		case <-ctx.Done():
			C.ng_cancel(e.api, e.handle)
		case <-finished:
		}
	}()
	var output *C.uint8_t
	var length C.size_t
	code := C.ng_detect(e.api, e.handle, (*C.uint8_t)(unsafe.Pointer(unsafe.StringData(text))), C.size_t(len(text)), &output, &length)
	runtime.KeepAlive(text)
	close(finished)
	<-cancelled
	if output != nil {
		defer C.ng_free(e.api, output, length)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := nativeError(int(code)); err != nil {
		return nil, err
	}
	if output == nil || length > 64<<20 {
		return nil, errors.New("隐私推理返回无效结果")
	}
	var spans []Span
	if err := json.Unmarshal(C.GoBytes(unsafe.Pointer(output), C.int(length)), &spans); err != nil {
		return nil, errors.New("隐私推理返回无效 JSON")
	}
	return validateSpans(ctx, text, spans)
}

func (e *Engine) Close() error {
	<-e.gate
	defer func() { e.gate <- struct{}{} }()
	if e.handle != nil {
		C.ng_close(e.api, e.handle)
		e.handle = nil
	}
	return nil
}
