//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// setupTray is a no-op until the macOS/Linux tray implementations land from
// their own branches.
func setupTray(_ *application.App, _ []byte) {}
