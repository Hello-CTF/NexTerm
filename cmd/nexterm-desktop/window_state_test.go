package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWindowStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := loadWindowState(dir); ok {
		t.Fatal("empty dir must have no saved state")
	}
	want := windowState{X: 40, Y: 60, Width: 1200, Height: 800, Maximised: true}
	if err := saveWindowState(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := loadWindowState(dir)
	if !ok || got != want {
		t.Fatalf("got %+v ok=%v, want %+v", got, ok, want)
	}
}

func TestApplyWindowState(t *testing.T) {
	base := application.WebviewWindowOptions{Width: 1480, Height: 920, MinWidth: 960, MinHeight: 600}
	screens := []application.Rect{{X: 0, Y: 0, Width: 1920, Height: 1080}}

	opts := applyWindowState(base, windowState{X: 100, Y: 50, Width: 1000, Height: 700}, screens)
	if opts.Width != 1000 || opts.Height != 700 || opts.InitialPosition != application.WindowXY || opts.X != 100 || opts.Y != 50 {
		t.Fatalf("visible state not restored: %+v", opts)
	}

	opts = applyWindowState(base, windowState{X: 5000, Y: 50, Width: 100, Height: 100, Maximised: true}, screens)
	if opts.InitialPosition == application.WindowXY {
		t.Fatal("off-screen position must not be reused")
	}
	if opts.Width != 960 || opts.Height != 600 {
		t.Fatalf("size must be clamped to minimum: %dx%d", opts.Width, opts.Height)
	}
	if opts.StartState != application.WindowStateMaximised {
		t.Fatal("maximised state not restored")
	}
}
