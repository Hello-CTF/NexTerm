// Package inference runs AstrLink's privacy engine in-process: a Go port of the
// AstrLink privacy worker that calls a bundled ONNX Runtime through its C API.
// The runtime library is packaged separately from downloaded weights.
package inference

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf8"
)

const MinimumScore float32 = 0.8

type Span struct {
	Start int     `json:"start"`
	End   int     `json:"end"`
	Label string  `json:"label"`
	Score float32 `json:"score"`
}

// PlatformError carries a bounded, user-visible operating system requirement.
type PlatformError struct{ Message string }

func (e *PlatformError) Error() string { return e.Message }

var ErrUnavailable = errors.New("本地隐私推理运行时未安装或不兼容")

// RuntimeError says why the packaged ONNX Runtime library cannot be used. It
// matches ErrUnavailable, so existing callers keep working, while Missing
// separates an absent file from a library the system refused to load (for
// example a code signature or library validation rejection on macOS).
type RuntimeError struct {
	Path    string
	Missing bool
	Err     error
}

func (e *RuntimeError) Error() string {
	if e.Missing {
		return "本地隐私推理运行时文件不存在: " + e.Path
	}
	return "本地隐私推理运行时无法使用: " + e.Path + ": " + e.Err.Error()
}
func (e *RuntimeError) Unwrap() error        { return e.Err }
func (e *RuntimeError) Is(target error) bool { return target == ErrUnavailable }

var ErrClosed = errors.New("本地隐私推理引擎已关闭")
var ErrTokenLimit = errors.New("隐私检测文本超过 131072 token 上限")
var errModelIncompatible = errors.New("隐私模型格式、文件或算子与运行时不兼容")

// DefaultRuntimeDirectory locates libraries shipped with this application.
// Android's executable is app_process, so the directory of the shared
// library holding this package is used.
func DefaultRuntimeDirectory() string {
	if runtime.GOOS == "android" {
		if path := selfLibraryPath(); path != "" {
			return filepath.Dir(path)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	directory := filepath.Dir(executable)
	if runtime.GOOS == "ios" {
		return filepath.Join(directory, "Frameworks")
	}
	if runtime.GOOS == "darwin" && filepath.Base(directory) == "MacOS" && filepath.Base(filepath.Dir(directory)) == "Contents" {
		return filepath.Join(filepath.Dir(directory), "Frameworks", "ai-runtime")
	}
	return filepath.Join(directory, "ai-runtime")
}

// RuntimeFiles returns the ONNX Runtime library path relative to a runtime
// directory, the only file the directory needs. iOS embeds a signed
// framework; Android's library resides in nativeLibraryDir.
func RuntimeFiles() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"onnxruntime.dll"}
	case "darwin":
		return []string{"libonnxruntime.dylib"}
	case "ios":
		return []string{"onnxruntime.framework/onnxruntime"}
	default:
		return []string{"libonnxruntime.so"}
	}
}

// runtimePath returns the absolute ONNX Runtime library path without loading it.
func runtimePath(directory string) (string, error) {
	if directory == "" {
		return "", ErrUnavailable
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, filepath.FromSlash(RuntimeFiles()[0]))
	info, err := os.Stat(path)
	if err != nil {
		return "", &RuntimeError{Path: path, Missing: errors.Is(err, os.ErrNotExist), Err: err}
	}
	if !info.Mode().IsRegular() {
		return "", &RuntimeError{Path: path, Missing: true, Err: errors.New("不是常规文件")}
	}
	return path, nil
}

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
