package diagcapture

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/platform"
)

func fastOptions() Options {
	return Options{
		CPUFirst: 50 * time.Millisecond, CPULaterAt: 80 * time.Millisecond, CPULater: 50 * time.Millisecond,
		TraceFor:      50 * time.Millisecond,
		GoroutineFast: 20 * time.Millisecond, GoroutineFastFor: 60 * time.Millisecond, GoroutineSlow: 40 * time.Millisecond,
		ProfileEvery: 30 * time.Millisecond, StatsEvery: 20 * time.Millisecond,
		MaxDuration: time.Minute, MaxBytes: 50 << 20,
		MutexFraction: 5, BlockRate: time.Millisecond,
	}
}

func newEnv(t *testing.T) (platform.Paths, *applog.Logger) {
	t.Helper()
	paths := platform.NewPaths(t.TempDir())
	logger, err := applog.NewLogger(paths, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return paths, logger
}

func TestArmDisarmAndConsume(t *testing.T) {
	paths, logger := newEnv(t)
	if IsArmed(paths) {
		t.Fatal("armed before Arm")
	}
	if err := Arm(paths); err != nil {
		t.Fatal(err)
	}
	if !IsArmed(paths) {
		t.Fatal("not armed after Arm")
	}
	if err := Disarm(paths); err != nil || IsArmed(paths) {
		t.Fatalf("disarm: %v armed=%v", err, IsArmed(paths))
	}
	if err := Disarm(paths); err != nil {
		t.Fatalf("disarming twice must be fine: %v", err)
	}
	_ = Arm(paths)
	s, err := Start(context.Background(), paths, logger, Info{Mode: "test", Source: "marker"}, fastOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if IsArmed(paths) {
		t.Fatal("marker must be consumed by Start")
	}
}

func TestMarkerConsumedWhenStartFails(t *testing.T) {
	paths, logger := newEnv(t)
	_ = Arm(paths)
	// A regular file where the capture root should be makes folder creation fail.
	if err := os.MkdirAll(paths.LogDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Root(paths), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), paths, logger, Info{}, fastOptions()); err == nil {
		t.Fatal("expected error")
	}
	if IsArmed(paths) {
		t.Fatal("marker must be deleted even when Start fails")
	}
}

func TestCaptureWritesArtifactsAndPermissions(t *testing.T) {
	paths, logger := newEnv(t)
	prevMutex := runtime.SetMutexProfileFraction(-1)
	s, err := Start(context.Background(), paths, logger, Info{Mode: "test", Source: "flag", Args: []string{"--db-dsn", "postgres://u:pw@h/db", "--listen=:1"}}, fastOptions())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s.Dir()) != Root(paths) {
		t.Fatalf("dir %s not under root", s.Dir())
	}
	logger.Debug("first connect probe", "tab_id", "t1", "elapsed_ms", 3)
	time.Sleep(400 * time.Millisecond)
	s.Stop()
	s.Stop() // idempotent
	for _, name := range []string{"meta.txt", "app.debug.log", "runtime-stats.jsonl", "capture-status.txt", "cpu-first.pprof", "trace-first-20s.out"} {
		info, err := os.Stat(filepath.Join(s.Dir(), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", name, info.Mode().Perm())
		}
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(s.Dir())
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", info.Mode().Perm())
		}
	}
	for _, sub := range []string{"goroutines", "profiles"} {
		items, err := os.ReadDir(filepath.Join(s.Dir(), sub))
		if err != nil || len(items) == 0 {
			t.Fatalf("%s: %v %d", sub, err, len(items))
		}
	}
	meta, _ := os.ReadFile(filepath.Join(s.Dir(), "meta.txt"))
	if strings.Contains(string(meta), "pw@") || strings.Contains(string(meta), "postgres://") {
		t.Fatalf("meta leaks DSN: %s", meta)
	}
	if !strings.Contains(string(meta), "--listen=:1") {
		t.Fatalf("meta missing args: %s", meta)
	}
	if got := runtime.SetMutexProfileFraction(-1); got != prevMutex {
		t.Fatalf("mutex fraction not restored: %d != %d", got, prevMutex)
	}
	if Active() != nil {
		t.Fatal("session still registered after Stop")
	}
}

func TestDebugLogRedactedAndLevelRestored(t *testing.T) {
	paths, logger := newEnv(t)
	s, err := Start(context.Background(), paths, logger, Info{}, fastOptions())
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("connecting", "password", "hunter2-secret", "note", "token=abc123secret")
	logger.With("tab_id", "t9").Debug("derived before and after", "elapsed_ms", 5)
	logger.Info("info line")
	s.Stop()
	debugLog, _ := os.ReadFile(filepath.Join(s.Dir(), "app.debug.log"))
	text := string(debugLog)
	if strings.Contains(text, "hunter2-secret") || strings.Contains(text, "abc123secret") {
		t.Fatalf("secret leaked into app.debug.log: %s", text)
	}
	if !strings.Contains(text, "connecting") || !strings.Contains(text, "tab_id=t9") || !strings.Contains(text, "info line") {
		t.Fatalf("debug log incomplete: %s", text)
	}
	mainLog, _ := os.ReadFile(paths.LogFile)
	if strings.Contains(string(mainLog), "connecting") {
		t.Fatalf("debug records must not reach the regular log: %s", mainLog)
	}
	if !strings.Contains(string(mainLog), "info line") {
		t.Fatalf("info record missing from regular log")
	}
	if logger.Logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug must be disabled again after Stop")
	}
	if logger.Level().Level() != slog.LevelInfo {
		t.Fatalf("level changed: %v", logger.Level().Level())
	}
	size := len(debugLog)
	logger.Debug("after stop")
	again, _ := os.ReadFile(filepath.Join(s.Dir(), "app.debug.log"))
	if len(again) != size {
		t.Fatal("tee still writing after Stop")
	}
}

func TestSizeCapStopsWrites(t *testing.T) {
	paths, logger := newEnv(t)
	o := fastOptions()
	o.MaxBytes = 4096
	s, err := Start(context.Background(), paths, logger, Info{}, o)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		logger.Debug(strings.Repeat("x", 100), "i", i)
	}
	s.Stop()
	if !s.Capped() {
		t.Fatal("expected capped")
	}
	status, _ := os.ReadFile(filepath.Join(s.Dir(), "capture-status.txt"))
	if !strings.Contains(string(status), "size_cap_reached: true") {
		t.Fatalf("status: %s", status)
	}
	info, _ := os.Stat(filepath.Join(s.Dir(), "app.debug.log"))
	if info.Size() > 8192 {
		t.Fatalf("debug log grew past cap: %d", info.Size())
	}
}

func TestMaxDurationStopsCapture(t *testing.T) {
	paths, logger := newEnv(t)
	o := fastOptions()
	o.MaxDuration = 100 * time.Millisecond
	s, err := Start(context.Background(), paths, logger, Info{}, o)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not stop at max duration")
	}
	status, _ := os.ReadFile(filepath.Join(s.Dir(), "capture-status.txt"))
	if !strings.Contains(string(status), "max duration") {
		t.Fatalf("status: %s", status)
	}
}

func TestContextCancelStopsCapture(t *testing.T) {
	paths, logger := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := Start(ctx, paths, logger, Info{}, fastOptions())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("capture did not stop on cancel")
	}
}

func TestSecondStartRejected(t *testing.T) {
	paths, logger := newEnv(t)
	s, err := Start(context.Background(), paths, logger, Info{}, fastOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if _, err := Start(context.Background(), paths, logger, Info{}, fastOptions()); err == nil {
		t.Fatal("expected second start to fail")
	}
}

func TestPackRejectsOutsideRoot(t *testing.T) {
	paths, _ := newEnv(t)
	root := Root(paths)
	good := filepath.Join(root, "20260101T000000Z")
	if err := os.MkdirAll(good, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "meta.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(paths.DataDir, "secret")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(good, "sub")
	_ = os.MkdirAll(nested, 0o700)
	bad := []string{
		"", ".", "..", root, outside,
		filepath.Join(root, "..", "secret"),
		filepath.Join(good, "..", "..", "secret"),
		"../secret", "../../" + filepath.Base(paths.DataDir),
		nested, filepath.Join(root, "missing"),
	}
	for _, dir := range bad {
		if _, err := Pack(paths, dir); err == nil {
			t.Errorf("Pack(%q) should be rejected", dir)
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		if err := os.Symlink(outside, link); err == nil {
			if _, err := Pack(paths, link); err == nil {
				t.Error("symlinked capture folder must be rejected")
			}
		}
	}
	archive, err := Pack(paths, good)
	if err != nil {
		t.Fatal(err)
	}
	if archive != good+".zip" {
		t.Fatalf("archive %s", archive)
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "20260101T000000Z/meta.txt" {
		t.Fatalf("zip entries: %+v", zr.File)
	}
	// Relative names are accepted when they are a direct child.
	if _, err := Pack(paths, "20260101T000000Z"); err != nil {
		t.Fatalf("relative direct child: %v", err)
	}
	entries, err := List(paths)
	if err != nil || len(entries) == 0 || entries[0].Archive == "" {
		t.Fatalf("list: %v %+v", err, entries)
	}
}
