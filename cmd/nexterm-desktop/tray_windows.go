//go:build windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// setupTray shows the NexTerm icon in the notification area. It has no menu
// or window behavior yet; closing the window still quits the app.
func setupTray(app *application.App, icon []byte) {
	tray := app.SystemTray.New()
	if len(icon) > 0 {
		tray.SetIcon(icon)
	}
	tray.SetTooltip("NexTerm")
}
