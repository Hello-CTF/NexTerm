//go:build windows

package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

type windowIcons struct {
	large w32.HICON
	small w32.HICON
}

func createWindowIcons(icon []byte) (windowIcons, error) {
	if len(icon) == 0 {
		return windowIcons{}, errors.New("desktop brand icon is empty")
	}
	large, largeErr := w32.CreateLargeHIconFromImage(icon)
	small, smallErr := w32.CreateSmallHIconFromImage(icon)
	return windowIcons{large: large, small: small}, errors.Join(largeErr, smallErr)
}

func setWindowIconWhenReady(window *application.WebviewWindow, icon []byte, logger *slog.Logger) {
	var once sync.Once
	window.OnWindowEvent(events.Common.WindowShow, func(_ *application.WindowEvent) {
		once.Do(func() {
			icons, err := createWindowIcons(icon)
			hwnd := w32.HWND(window.NativeWindow())
			if hwnd == 0 {
				logger.Warn("set window icon", "error", errors.Join(err, errors.New("desktop window handle is unavailable")))
				return
			}
			if icons.large != 0 {
				w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(icons.large))
			}
			if icons.small != 0 {
				w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(icons.small))
			}
			if err != nil {
				logger.Warn("set window icon", "error", err)
			}
		})
	})
}

// trayQuitting marks a real quit (tray menu, updater restart, app shutdown) so
// the close hook lets the window close instead of hiding it.
var trayQuitting atomic.Bool

// setupTray shows the NexTerm icon in the notification area and optionally
// keeps the app running after the window is closed.
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
	menu.Add("退出 NexTerm").OnClick(func(_ *application.Context) {
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
		enabled, err := production.DesktopCloseToTray(ctx, database)
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
	if window.IsMinimised() {
		window.UnMinimise()
	}
	window.Focus()
}
