package privacy

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy/inference"
)

func TestUnavailableMessageSeparatesMissingFromLoadRefusal(t *testing.T) {
	missing := &inference.RuntimeError{Path: "/app/ai-runtime/libonnxruntime.dylib", Missing: true, Err: os.ErrNotExist}
	refused := &inference.RuntimeError{Path: "/app/ai-runtime/libonnxruntime.dylib", Err: errors.New("code signature not valid for use in process")}
	for _, err := range []error{missing, refused} {
		if !errors.Is(err, inference.ErrUnavailable) {
			t.Fatalf("%v does not match ErrUnavailable", err)
		}
	}
	if got := UnavailableMessage(missing); !strings.Contains(got, "文件不存在") || !strings.Contains(got, missing.Path) {
		t.Fatalf("missing: %q", got)
	}
	if got := UnavailableMessage(refused); !strings.Contains(got, "系统拒绝加载") || !strings.Contains(got, "code signature") || strings.Contains(got, "文件不存在") {
		t.Fatalf("refused: %q", got)
	}
	if got := UnavailableMessage(inference.ErrUnavailable); got != RuntimeUnavailableMessage() {
		t.Fatalf("plain: %q", got)
	}
}
