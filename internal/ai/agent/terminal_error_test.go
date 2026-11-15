package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
)

func TestTerminalErrorMessageShowsProviderDetail(t *testing.T) {
	t.Parallel()
	rejected := fmt.Errorf("chat: %w", &provider.HTTPError{
		Operation: "chat", StatusCode: 400,
		Body: "Invalid parameter: messages with role 'assistant' must have content or tool_calls; api_key=sk-live-123",
	})
	got := terminalErrorMessage(rejected)
	for _, want := range []string{
		"AI 服务拒绝了请求，请检查模型档案中的密钥与模型配置（服务端返回 HTTP 400：",
		"messages with role 'assistant' must have content or tool_calls",
		"api_key=<redacted>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("message %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "sk-live-123") {
		t.Fatalf("message leaks the key: %q", got)
	}

	if got := terminalErrorMessage(&provider.HTTPError{Operation: "chat", StatusCode: 503}); got != "AI 服务暂时不可用（服务端错误），请稍后重试（服务端返回 HTTP 503）" {
		t.Fatalf("message without a body = %q", got)
	}
	if got := terminalErrorMessage(errors.New("plain failure")); got != "plain failure" {
		t.Fatalf("unclassified message = %q", got)
	}
}
