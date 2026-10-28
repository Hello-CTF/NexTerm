// Package inference runs AstrLink's privacy engine in-process through its native
// C ABI. The runtime libraries are packaged separately from downloaded weights.
package inference

import (
	"context"
	"errors"
	"fmt"
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
var ErrClosed = errors.New("本地隐私推理引擎已关闭")
var ErrTokenLimit = errors.New("隐私检测文本超过 131072 token 上限")

// DefaultRuntimeDirectory locates libraries shipped with this application.
// Android's executable is app_process, so its CGO bridge location is used.
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

// RuntimeFiles returns paths relative to a runtime directory. iOS libraries are
// embedded and signed frameworks; Android libraries reside in nativeLibraryDir.
func RuntimeFiles() (engine, onnx string) {
	switch runtime.GOOS {
	case "windows":
		return "nexterm_privacy.dll", "onnxruntime.dll"
	case "darwin":
		return "libnexterm_privacy.dylib", "libonnxruntime.dylib"
	case "ios":
		return "NexTermPrivacy.framework/NexTermPrivacy", "onnxruntime.framework/onnxruntime"
	default:
		return "libnexterm_privacy.so", "libonnxruntime.so"
	}
}

func runtimePaths(directory string) (string, string, error) {
	if directory == "" {
		return "", "", ErrUnavailable
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", "", err
	}
	engine, onnx := RuntimeFiles()
	engine = filepath.Join(directory, engine)
	onnx = filepath.Join(directory, onnx)
	for _, path := range []string{engine, onnx} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", "", ErrUnavailable
		}
	}
	return engine, onnx, nil
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
		switch span.Label {
		case "email", "phone", "account", "payment_card", "ip_address", "url", "common_secret", "private_address", "private_date", "private_person":
		default:
			return nil, errors.New("隐私模型返回未知标签")
		}
		if span.Score >= MinimumScore {
			accepted = append(accepted, span)
		}
	}
	return accepted, nil
}

func nativeError(code int) error {
	switch code {
	case 0:
		return nil
	case 1:
		return errors.New("隐私推理输入无效")
	case 2:
		return ErrUnavailable
	case 3:
		return errors.New("隐私模型格式、文件或算子与运行时不兼容")
	case 4:
		return ErrTokenLimit
	case 6:
		return context.Canceled
	default:
		return fmt.Errorf("本地隐私推理失败（代码 %d）", code)
	}
}
