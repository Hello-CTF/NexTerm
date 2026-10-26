//go:build !windows

package main

import (
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// applyAppIcon leaves Options.Icon unset: macOS uses the bundle's icon.icns
// and GTK4 resolves the icon through the desktop entry matching the app id.
func applyAppIcon(_ *application.Options, _ []byte) {}

func setWindowIconWhenReady(_ *application.WebviewWindow, _ []byte, _ *slog.Logger) {
}
