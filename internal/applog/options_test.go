package applog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestParseEnvDefaultsAndValidValues(t *testing.T) {
	options, err := ParseEnv(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if options.Level != slog.LevelInfo || options.Format != FormatText || options.File != FileAuto {
		t.Fatalf("defaults = %+v", options)
	}
	options, err = ParseEnv(envMap(map[string]string{
		EnvLogLevel: " Debug ", EnvLogFormat: "JSON", EnvLogFile: "off", "JOURNAL_STREAM": "8:123",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if options.Level != slog.LevelDebug || options.Format != FormatJSON || options.File != FileOff || options.JournalStream != "8:123" {
		t.Fatalf("parsed = %+v", options)
	}
	for raw, want := range map[string]slog.Level{"info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := ParseLevel(raw)
		if err != nil || got != want {
			t.Fatalf("ParseLevel(%q) = %v, %v", raw, got, err)
		}
	}
}

func TestParseEnvReportsEveryInvalidValueAndKeepsDefaults(t *testing.T) {
	options, err := ParseEnv(envMap(map[string]string{
		EnvLogLevel: "verbose", EnvLogFormat: "xml", EnvLogFile: "maybe",
	}))
	if err == nil {
		t.Fatal("invalid values were accepted")
	}
	for _, name := range []string{EnvLogLevel, EnvLogFormat, EnvLogFile} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	if options.Level != slog.LevelInfo || options.Format != FormatText || options.File != FileAuto {
		t.Fatalf("invalid values changed defaults: %+v", options)
	}
}

func TestLoggerHonorsLevelAndJSONFormat(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	var console bytes.Buffer
	options := DefaultOptions()
	options.Level = slog.LevelWarn
	options.Format = FormatJSON
	logger, err := NewLoggerWithOptions(paths, &console, options)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	logger.Info("hidden")
	logger.Warn("shown", "token", "token-value")
	if strings.Contains(console.String(), "hidden") {
		t.Fatalf("info record passed a warn level: %q", console.String())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(console.Bytes()), &record); err != nil {
		t.Fatalf("console is not one JSON record: %v: %q", err, console.String())
	}
	if record["msg"] != "shown" || record["token"] != "***" {
		t.Fatalf("record = %v", record)
	}
}

func TestLoggerFileModeOffWritesConsoleOnly(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	var console bytes.Buffer
	options := DefaultOptions()
	options.File = FileOff
	logger, err := NewLoggerWithOptions(paths, &console, options)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("console-only")
	_ = logger.Close()
	if logger.FilePath() != "" {
		t.Fatalf("FilePath = %q", logger.FilePath())
	}
	if _, err := os.Stat(paths.LogFile); !os.IsNotExist(err) {
		t.Fatalf("log file exists with file mode off: %v", err)
	}
	if !strings.Contains(console.String(), "console-only") {
		t.Fatalf("console = %q", console.String())
	}
}

func TestLoggerAutoFileSkipsOnlyWhenStderrIsTheJournal(t *testing.T) {
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	journal := journalStreamOf(t, stderr)

	for _, tc := range []struct {
		name     string
		skip     bool
		stream   string
		wantFile bool
	}{
		{name: "server under journald", skip: true, stream: journal, wantFile: journal == ""},
		{name: "desktop under journald", skip: false, stream: journal, wantFile: true},
		{name: "inherited variable, other stderr", skip: true, stream: "1:1", wantFile: true},
		{name: "no journal", skip: true, stream: "", wantFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := platform.NewPaths(t.TempDir())
			options := DefaultOptions()
			options.SkipFileUnderJournal = tc.skip
			options.JournalStream = tc.stream
			logger, err := NewLoggerWithOptions(paths, stderr, options)
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			if got := logger.FilePath() != ""; got != tc.wantFile {
				t.Fatalf("file sink = %v, want %v", got, tc.wantFile)
			}
		})
	}
}

func TestRotatingFileKeepsBoundedBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexterm.log")
	if err := os.WriteFile(path, []byte("existing-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openRotatingFile(path, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := fmt.Fprintf(file, "line-%d-0123456789\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	// Each 18-byte line plus the 20-byte seed exceeds 32 bytes, so every write starts a new file.
	if got := read(path); got != "line-5-0123456789\n" {
		t.Fatalf("current = %q", got)
	}
	if got := read(path + ".1"); got != "line-4-0123456789\n" {
		t.Fatalf("backup 1 = %q", got)
	}
	if got := read(path + ".2"); got != "line-3-0123456789\n" {
		t.Fatalf("backup 2 = %q", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("backup beyond the limit exists: %v", err)
	}
}

func TestRotatingFileAppendsUntilLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexterm.log")
	file, err := openRotatingFile(path, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"aaaaaaaaaa\n", "bbbbbbbbbb\n", "cccccccccc\n"} {
		if _, err := file.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	_ = file.Close()
	data, _ := os.ReadFile(path)
	if string(data) != "aaaaaaaaaa\nbbbbbbbbbb\ncccccccccc\n" {
		t.Fatalf("current = %q", data)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotated below the limit: %v", err)
	}
}

func TestLoggerRotatesLogFile(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	options := DefaultOptions()
	options.MaxFileBytes = 256
	options.MaxBackups = 1
	logger, err := NewLoggerWithOptions(paths, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		logger.Info("rotation record", "index", i, "padding", strings.Repeat("x", 40))
	}
	_ = logger.Close()
	current, err := os.ReadFile(paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(paths.LogFile + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) > 256 || len(backup) > 256 {
		t.Fatalf("log sizes exceed the limit: current=%d backup=%d", len(current), len(backup))
	}
	if !strings.Contains(string(current), "index=19") {
		t.Fatalf("latest record missing from current log: %q", current)
	}
	if _, err := os.Stat(paths.LogFile + ".2"); !os.IsNotExist(err) {
		t.Fatalf("backup beyond the limit exists: %v", err)
	}
}
