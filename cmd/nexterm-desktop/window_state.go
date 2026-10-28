package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	wailsevents "github.com/wailsapp/wails/v3/pkg/events"
)

const (
	windowStateFile     = "window-state.json"
	windowStateDebounce = 400 * time.Millisecond
)

// windowState is the last normal (non-maximised) window geometry plus whether the
// window was maximised, persisted next to the database.
type windowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

func loadWindowState(dataDir string) (windowState, bool) {
	raw, err := os.ReadFile(filepath.Join(dataDir, windowStateFile))
	if err != nil {
		return windowState{}, false
	}
	var state windowState
	if err := json.Unmarshal(raw, &state); err != nil || state.Width <= 0 || state.Height <= 0 {
		return windowState{}, false
	}
	return state, true
}

func saveWindowState(dataDir string, state windowState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, windowStateFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// applyWindowState overlays a saved state on the default options. The position is
// only reused when the window still overlaps a connected screen, so an unplugged
// monitor cannot leave it off-screen; the size is always reused, clamped to the minimum.
func applyWindowState(opts application.WebviewWindowOptions, state windowState, screens []application.Rect) application.WebviewWindowOptions {
	opts.Width = max(state.Width, opts.MinWidth)
	opts.Height = max(state.Height, opts.MinHeight)
	for _, screen := range screens {
		if overlaps(application.Rect{X: state.X, Y: state.Y, Width: opts.Width, Height: opts.Height}, screen) {
			opts.InitialPosition = application.WindowXY
			opts.X = state.X
			opts.Y = state.Y
			break
		}
	}
	if state.Maximised {
		opts.StartState = application.WindowStateMaximised
	}
	return opts
}

// overlaps requires a usable strip of the window (its top-left area) to be on screen.
func overlaps(win, screen application.Rect) bool {
	const margin = 80
	return win.X+win.Width > screen.X+margin && win.X < screen.X+screen.Width-margin &&
		win.Y >= screen.Y-margin && win.Y < screen.Y+screen.Height-margin
}

func screenBounds(app *application.App) []application.Rect {
	var rects []application.Rect
	for _, screen := range app.Screen.GetAll() {
		rects = append(rects, screen.Bounds)
	}
	return rects
}

// trackWindowState saves geometry shortly after the last move, resize or maximise
// change, and once more when the application shuts down.
func trackWindowState(window *application.WebviewWindow, dataDir string, initial windowState, report func(error)) {
	var mu sync.Mutex
	state := initial
	var timer *time.Timer
	capture := func() {
		mu.Lock()
		defer mu.Unlock()
		if window.IsMinimised() || window.IsFullscreen() {
			return
		}
		state.Maximised = window.IsMaximised()
		if !state.Maximised {
			b := window.Bounds()
			if b.Width <= 0 || b.Height <= 0 {
				return
			}
			state.X, state.Y, state.Width, state.Height = b.X, b.Y, b.Width, b.Height
		}
		if state.Width <= 0 || state.Height <= 0 {
			return
		}
		if err := saveWindowState(dataDir, state); err != nil && report != nil {
			report(err)
		}
	}
	schedule := func(_ *application.WindowEvent) {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(windowStateDebounce, capture)
	}
	for _, event := range []wailsevents.WindowEventType{
		wailsevents.Common.WindowDidMove,
		wailsevents.Common.WindowDidResize,
		wailsevents.Common.WindowMaximise,
		wailsevents.Common.WindowUnMaximise,
		wailsevents.Common.WindowRestore,
	} {
		window.OnWindowEvent(event, schedule)
	}
	window.RegisterHook(wailsevents.Common.WindowClosing, func(_ *application.WindowEvent) { capture() })
}
