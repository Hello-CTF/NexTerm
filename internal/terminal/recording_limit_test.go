package terminal

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRecordingLimitedStopsAtCapAndFailsVisibly(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "capped.log")
	var released atomic.Int32
	if err := tab.StartRecordingLimited(path, 8, func() { released.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(t.Context(), []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(t.Context(), []byte("67890")); err != nil {
		t.Fatal(err)
	}
	n, err := tab.StopRecording()
	if !errors.Is(err, ErrRecordingTooLarge) || n != 8 {
		t.Fatalf("StopRecording = (%d, %v), want (8, ErrRecordingTooLarge)", n, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "12345678" {
		t.Fatalf("recorded = %q, %v; want the bytes up to the cap", data, err)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("release ran %d times, want once after the file closed", got)
	}
}

func TestRecordingWithoutLimitKeepsEverythingAndReleasesOnStop(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "unlimited.log")
	var released atomic.Int32
	if err := tab.StartRecordingLimited(path, 0, func() { released.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(t.Context(), []byte("1234567890")); err != nil {
		t.Fatal(err)
	}
	if n, err := tab.StopRecording(); err != nil || n != 10 {
		t.Fatalf("StopRecording = (%d, %v), want (10, nil)", n, err)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("release ran %d times after stop, want once", got)
	}
}

func TestRecordingLimitedReleasesWhenTheFileCannotOpen(t *testing.T) {
	tab := newTestTab(t)
	var released atomic.Int32
	err := tab.StartRecordingLimited(filepath.Join(t.TempDir(), "missing", "x.log"), 8, func() { released.Add(1) })
	if err == nil || released.Load() != 1 {
		t.Fatalf("start = %v with %d releases, want an error and one release", err, released.Load())
	}
}
