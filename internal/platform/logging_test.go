package platform

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathsPrepareAndLoggerWriteFileAndConsole(t *testing.T) {
	paths := NewPaths(filepath.Join(t.TempDir(), "data"))
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
	paths := NewPaths(root)
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
