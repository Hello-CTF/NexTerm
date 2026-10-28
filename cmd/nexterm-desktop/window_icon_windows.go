//go:build windows

package main

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// applyAppIcon hands the brand icon to Wails so windows get a big icon at
// creation: the embedded icon group is resource ID 1, but Wails only looks up
// ID 3 before falling back to Options.Icon.
func applyAppIcon(options *application.Options, icon []byte) {
	if len(icon) > 0 {
		options.Icon = icon
	}
}

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

// setWindowIconWhenReady sets both icon sizes once the native window exists;
// Wails only sets the big icon, so the title bar would show it scaled down.
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
			application.InvokeSync(func() {
				if icons.large != 0 {
					w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(icons.large))
				}
				if icons.small != 0 {
					w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(icons.small))
				}
			})
			if err != nil {
				logger.Warn("set window icon", "error", err)
			}
		})
	})
}
