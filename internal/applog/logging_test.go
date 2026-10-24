package applog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

type logSecretStringer struct{}

func (logSecretStringer) String() string { return "https://user:secret-value@example.invalid" }

func TestPathsPrepareAndLoggerWriteFileAndConsole(t *testing.T) {
	paths := platform.NewPaths(filepath.Join(t.TempDir(), "data"))
	if err := paths.Prepare(); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	logger, err := NewLogger(paths, &console)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("skeleton-ready", "version", "test")
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "skeleton-ready") || !strings.Contains(console.String(), "skeleton-ready") {
		t.Fatalf("file=%q console=%q", contents, console.String())
	}
}

func TestLoggerFallsBackToConsole(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := platform.NewPaths(root)
	paths.LogDir = blocked
	paths.LogFile = filepath.Join(blocked, "nexterm.log")
	var console bytes.Buffer
	logger, err := NewLogger(paths, &console)
	if err == nil {
		t.Fatal("NewLogger unexpectedly opened the log file")
	}
	logger.Warn("console-fallback")
	if !strings.Contains(console.String(), "console-fallback") {
		t.Fatalf("console = %q", console.String())
	}
}

func TestLoggerRedactsMessageAndStructuredAttributes(t *testing.T) {
	paths := platform.NewPaths(filepath.Join(t.TempDir(), "data"))
	var console bytes.Buffer
	logger, err := NewLogger(paths, &console)
	if err != nil {
		t.Fatal(err)
	}
	logger.WithGroup("outer").With("password", "password-value", "version", "test").Info(
		"Authorization: Bearer bearer-value",
		slog.Group("details", "token", "token-value", "error", errors.New("password=error-value")),
		"stringer", logSecretStringer{},
		"password=key-value", "value",
	)
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{console.String(), string(file)} {
		for _, secret := range []string{"password-value", "bearer-value", "token-value", "error-value", "secret-value", "key-value"} {
			if strings.Contains(output, secret) {
				t.Errorf("log output contains %q", secret)
			}
		}
		for _, expected := range []string{"outer.version=test", "outer.password=***", "outer.details.token=***", "outer.details.error=\"password=***\"", "outer.stringer=https://user:***@example.invalid"} {
			if !strings.Contains(output, expected) {
				t.Errorf("log output is missing %q: %q", expected, output)
			}
		}
	}
}

func TestLoggerRedactsValuesInsideSensitiveWithGroup(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewTextHandler(&output, nil)))
	logger.WithGroup("credentials").With("value", "bound-secret").WithGroup("nested").Info(
		"sensitive group", "detail", "record-secret", "count", 7,
	)
	text := output.String()
	for _, secret := range []string{"bound-secret", "record-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("sensitive group leaked %q", secret)
		}
	}
	for _, expected := range []string{"credentials.value=***", "credentials.nested.detail=***", "credentials.nested.count=***"} {
		if !strings.Contains(text, expected) {
			t.Errorf("missing redacted field %q: %q", expected, text)
		}
	}
}

func TestDebugTeeDoesNotDoubleLogAndReachesEarlierDerivedLoggers(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	logger, err := NewLogger(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	derived := logger.With("tab_id", "t1")
	derived.Debug("before tee")
	var tee bytes.Buffer
	detach := logger.AttachDebugTee(&tee)
	derived.Debug("debug line", "password", "hunter2")
	derived.Info("info line")
	detach()
	derived.Debug("after detach")

	got := tee.String()
	if !strings.Contains(got, "debug line") || !strings.Contains(got, "tab_id=t1") || strings.Contains(got, "hunter2") {
		t.Fatalf("tee output = %q", got)
	}
	if strings.Contains(got, "before tee") || strings.Contains(got, "after detach") {
		t.Fatalf("tee output outside the attach window: %q", got)
	}
	if strings.Count(got, "info line") != 1 {
		t.Fatalf("info line must appear once in the tee: %q", got)
	}
	file, _ := os.ReadFile(paths.LogFile)
	if strings.Count(string(file), "info line") != 1 || strings.Contains(string(file), "debug line") {
		t.Fatalf("regular log changed by tee: %q", file)
	}
}

func TestDebugTeeDropsRecordsCarryingPayloads(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	logger, err := NewLogger(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	var tee bytes.Buffer
	detach := logger.AttachDebugTee(&tee)
	defer detach()

	logger.Debug("Binding call complete:", "args", `{"cmd":"terminal_write","data":[108,115]}`, "result", "ok")
	logger.Debug("timing only", "elapsed_ms", 12)
	logger.Warn("kept at warn", "args", "still allowed above debug")

	got := tee.String()
	if strings.Contains(got, "terminal_write") || strings.Contains(got, "Binding call") {
		t.Fatalf("payload record leaked into the capture: %q", got)
	}
	for _, want := range []string{"timing only", "kept at warn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("capture is missing %q: %q", want, got)
		}
	}
}

func wailsCall(logger *slog.Logger, id, args, result string, gap time.Duration) {
	start := time.Unix(1_700_000_000, 0)
	started := slog.NewRecord(start, slog.LevelDebug, "Binding call started:", 0)
	started.AddAttrs(slog.String("id", id), slog.String("method", "123"))
	_ = logger.Handler().Handle(context.Background(), started)
	complete := slog.NewRecord(start.Add(gap), slog.LevelDebug, "Binding call complete:", 0)
	complete.AddAttrs(slog.String("id", id), slog.String("method", "main.Service.Call"), slog.String("args", args), slog.String("result", result))
	_ = logger.Handler().Handle(context.Background(), complete)
}

func TestWailsLoggerRecordsASanitizedIPCTimeline(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	logger, err := NewLogger(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	wails := logger.WailsLogger()

	// Without a capture the framework's debug output is disabled entirely.
	if wails.Handler().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("wails debug logging must stay off while no capture is attached")
	}
	var tee bytes.Buffer
	detach := logger.AttachDebugTee(&tee)
	defer detach()
	if !wails.Handler().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("wails debug logging must be enabled during a capture")
	}

	wailsCall(wails, "c1", `[{"cmd":"terminal_write","args":{"args":{"data":[108,115,13]}}}]`, `{"ok":true,"data":null}`, 7*time.Millisecond)
	wailsCall(wails, "c2", `[{"cmd":"vault_unlock","args":{"password":"hunter2"}}]`, `{"ok":false,"error":{"code":"forbidden","message":"wrong secret hunter2"}}`, 30*time.Millisecond)
	wails.Debug("handleWebViewRequest: Processing request", "url", "wails://localhost/wails/runtime")
	wails.Debug("Asset Request:", "path", "/wails/runtime", "code", int64(200), "duration", time.Millisecond)
	wails.Debug("Asset Request:", "path", "/slow", "code", int64(200), "duration", 80*time.Millisecond)
	wails.Debug("Registering bound method:", "name", "main.Service.Call")
	wails.Warn("framework warning kept")

	got := tee.String()
	for _, forbidden := range []string{"108", "hunter2", "password", "terminal_write\",", "Processing request", "/wails/runtime", "data"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("capture leaked %q: %q", forbidden, got)
		}
	}
	for _, want := range []string{
		"cmd=terminal_write", "elapsed_ms=7", "ok=true",
		"cmd=vault_unlock", "elapsed_ms=30", "ok=false", "err_code=forbidden",
		"args_bytes=", "result_bytes=", "path=/slow", "Registering bound method:", "framework warning kept",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("capture is missing %q: %q", want, got)
		}
	}
}

func TestWailsCallCommandIgnoresNestedCmdFields(t *testing.T) {
	for args, want := range map[string]string{
		`[{"cmd":"terminal_list","args":{}}]`:                    "terminal_list",
		`[{"args":{"cmd":"rm_rf"},"cmd":"x"}]`:                   "unknown",
		`[{"cmd":"exec_batch_run","args":{"cmd":"cat_secret"}}]`: "exec_batch_run",
		`not json {"cmd":"sneaky"}`:                              "unknown",
		`[{"cmd":"Has Space","args":{}}]`:                        "unknown",
	} {
		if got := callCommand(args); got != want {
			t.Errorf("callCommand(%q) = %q, want %q", args, got, want)
		}
	}
}
