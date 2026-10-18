//go:build windows

package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// trayQuitting marks a real quit (tray menu, updater restart, app shutdown) so
// the close hook lets the window close instead of hiding it.
var trayQuitting atomic.Bool

// setupTray shows the NexTerm icon in the notification area with a minimal
// menu, and hides the window instead of closing it when close-to-tray is
// enabled (the default).
func setupTray(app *application.App, window *application.WebviewWindow, icon []byte, database *store.Store, logger *slog.Logger) {
	tray := app.SystemTray.New()
	if len(icon) > 0 {
		tray.SetIcon(icon)
	}
	tray.SetTooltip("NexTerm")

	menu := application.NewMenu()
	menu.Add("打开界面").OnClick(func(_ *application.Context) {
		showWindow(window)
	})
	menu.AddSeparator()
	menu.Add("彻底关闭").OnClick(func(_ *application.Context) {
		trayQuitting.Store(true)
		app.Quit()
	})
	tray.SetMenu(menu)

	tray.OnClick(func() {
		if window.IsVisible() {
			window.Hide()
		} else {
			showWindow(window)
		}
	})

	// Hooks run synchronously before Wails' own close listeners, so cancelling
	// here skips the built-in destroy; the event arrives on a separate goroutine,
	// so this bounded lookup cannot stall the UI thread.
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if trayQuitting.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		enabled, _, err := production.DesktopCloseToTray(ctx, database)
		if err != nil {
			logger.Warn("close-to-tray setting unreadable; allowing close", "error", err)
			return
		}
		if !enabled {
			return
		}
		window.Hide()
		event.Cancel()
	})

	app.OnShutdown(func() { trayQuitting.Store(true) })
}

// showWindow brings a hidden or minimized window back; shared by the tray
// menu/click and the second-instance and deep-link callbacks in main.go.
func showWindow(window *application.WebviewWindow) {
	window.Show()
	window.Restore()
	window.Focus()
}
