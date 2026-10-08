package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func warnIfExposedOutput(t *testing.T, address string, syncOnly bool, auth ...string) (string, string) {
	t.Helper()
	var logOutput, stderr bytes.Buffer
	WarnIfExposed(slog.New(slog.NewTextHandler(&logOutput, nil)), &stderr, address, syncOnly, auth...)
	return logOutput.String(), stderr.String()
}

func TestWarnIfExposedAuthOffNeverClaimsAccessControl(t *testing.T) {
	_, stderr := warnIfExposedOutput(t, "0.0.0.0:8080", false, AuthOff)
	if !strings.Contains(stderr, "WARNING: NexTerm is listening on non-loopback address 0.0.0.0:8080") || !strings.Contains(stderr, "--auth=off") {
		t.Fatalf("auth=off full warning = %q", stderr)
	}
	for _, lie := range []string{"已启用访问控制", "要求账号会话", "持有账号会话"} {
		if strings.Contains(stderr, lie) {
			t.Fatalf("auth=off full warning claims access control via %q: %q", lie, stderr)
		}
	}

	_, stderr = warnIfExposedOutput(t, "0.0.0.0:8080", true, AuthOff)
	if !strings.Contains(stderr, "onlyServer 模式") || !strings.Contains(stderr, "--auth=off") {
		t.Fatalf("auth=off sync-only warning = %q", stderr)
	}
	for _, lie := range []string{"持有账号会话的人可以同步", "已启用访问控制", "要求账号会话"} {
		if strings.Contains(stderr, lie) {
			t.Fatalf("auth=off sync-only warning claims session-gated sync via %q: %q", lie, stderr)
		}
	}
}

func TestWarnIfExposedEnabledModesRetainAccurateWarnings(t *testing.T) {
	logOutput, stderr := warnIfExposedOutput(t, "0.0.0.0:8080", false, AuthOn)
	if !strings.Contains(stderr, "完整版已启用访问控制") || !strings.Contains(stderr, "要求账号会话") {
		t.Fatalf("auth=on full warning = %q", stderr)
	}
	if !strings.Contains(logOutput, "auth=on") {
		t.Fatalf("auth=on logger output = %q", logOutput)
	}

	_, stderr = warnIfExposedOutput(t, "0.0.0.0:8080", true, AuthOn)
	if !strings.Contains(stderr, "持有账号会话的人可以同步") {
		t.Fatalf("auth=on sync-only warning = %q", stderr)
	}

	// loopback 模式监听非回环地址等同 on, 必须保留 on 口径而不是谎称免登录。
	_, stderr = warnIfExposedOutput(t, "0.0.0.0:8080", false, AuthLoopback)
	if !strings.Contains(stderr, "完整版已启用访问控制") || !strings.Contains(stderr, "要求账号会话") {
		t.Fatalf("auth=loopback exposed full warning = %q", stderr)
	}
	_, stderr = warnIfExposedOutput(t, "0.0.0.0:8080", true, AuthLoopback)
	if !strings.Contains(stderr, "持有账号会话的人可以同步") {
		t.Fatalf("auth=loopback exposed sync-only warning = %q", stderr)
	}

	// 调用方未传 auth 时保持 on 口径(与旧调用方行为一致)。
	_, stderr = warnIfExposedOutput(t, "0.0.0.0:8080", false)
	if !strings.Contains(stderr, "完整版已启用访问控制") {
		t.Fatalf("default full warning = %q", stderr)
	}
	_, stderr = warnIfExposedOutput(t, "0.0.0.0:8080", true)
	if !strings.Contains(stderr, "持有账号会话的人可以同步") {
		t.Fatalf("default sync-only warning = %q", stderr)
	}
}

func TestWarnIfExposedLoopbackAddressSilentForAllAuthModes(t *testing.T) {
	for _, mode := range []string{AuthOn, AuthLoopback, AuthOff} {
		for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
			logOutput, stderr := warnIfExposedOutput(t, address, false, mode)
			if logOutput != "" || stderr != "" {
				t.Fatalf("%s on %s warned: log=%q stderr=%q", mode, address, logOutput, stderr)
			}
		}
	}
}
