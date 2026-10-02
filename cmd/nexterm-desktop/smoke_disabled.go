//go:build !smoke

package main

import (
	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var desktopSmokeModules []core.Module

func desktopSmokeEnabled() bool { return false }

func runDesktopSmoke(*application.App, *application.WebviewWindow) int { return 0 }
