//go:build !windows

package main

import (
	"log/slog"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// setupTray is a no-op until the macOS/Linux tray implementations land from
// their own branches.
func setupTray(_ *application.App, _ *application.WebviewWindow, _ []byte, _ *store.Store, _ *slog.Logger) {
}

// showWindow keeps the shared window-restore behavior available on every
// platform; see tray_windows.go.
func showWindow(window *application.WebviewWindow) {
	window.Show()
	window.Restore()
	window.Focus()
}
