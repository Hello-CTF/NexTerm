package terminal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordingLimitedStopsAtCapAndFailsVisibly(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "capped.log")
	if err := tab.StartRecordingLimited(path, 8); err != nil {
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
}

func TestRecordingWithoutLimitKeepsEverything(t *testing.T) {
	tab := newTestTab(t)
	o := newTestOutput(t, tab)
	path := filepath.Join(t.TempDir(), "unlimited.log")
	if err := tab.StartRecording(path); err != nil {
		t.Fatal(err)
	}
	if err := o.Chunk(t.Context(), []byte("1234567890")); err != nil {
		t.Fatal(err)
	}
	if n, err := tab.StopRecording(); err != nil || n != 10 {
		t.Fatalf("StopRecording = (%d, %v), want (10, nil)", n, err)
	}
}
