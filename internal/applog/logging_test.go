package applog

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
