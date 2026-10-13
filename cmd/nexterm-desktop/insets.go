package main

import (
	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func currentScreen(window *application.WebviewWindow) *application.Screen {
	if window != nil {
		if screen, err := window.GetScreen(); err == nil && screen != nil {
			return screen
		}
	}
	return application.Get().Screen.GetPrimary()
}

func screenInsets(screen *application.Screen) production.ScreenInsets {
	if screen == nil {
		return production.ScreenInsets{}
	}
	return production.ScreenInsets{
		Top:    screen.WorkArea.Y - screen.Bounds.Y,
		Bottom: screen.Bounds.Height - screen.WorkArea.Y - screen.WorkArea.Height,
		Left:   screen.WorkArea.X - screen.Bounds.X,
		Right:  screen.Bounds.Width - screen.WorkArea.X - screen.WorkArea.Width,
	}
}
