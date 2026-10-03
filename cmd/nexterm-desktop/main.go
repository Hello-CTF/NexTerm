package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	production "github.com/ProbiusOfficial/NexTerm/internal/app/production"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	invocation, err := core.ParseCLI(args, core.CommandDesktop, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", err)
		return 2
	}
	if invocation.Help {
		fmt.Fprint(os.Stdout, core.Usage("nexterm-desktop", core.CommandDesktop))
		return 0
	}
	if invocation.Version {
		fmt.Fprintln(os.Stdout, version.Version)
		return 0
	}
	if invocation.Command != core.CommandDesktop {
		fmt.Fprintln(os.Stderr, "nexterm-desktop: use the nexterm-server binary for", invocation.Command)
		return 2
	}

	if desktopSmokeEnabled() && runtime.GOOS == "linux" {
		// CI runners restrict the unprivileged namespaces that WebKitGTK's
		// bubblewrap sandbox needs ("bwrap: loopback: Failed RTM_NEWADDR"),
		// which kills the web process before it can produce smoke evidence.
		// The smoke-tagged binary exists only for this gate, so its web process
		// may run unsandboxed; production binaries keep the sandbox.
		os.Setenv("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS", "1")
	}

	paths, err := platform.DesktopPaths(invocation.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", err)
		return 1
	}
	if err := paths.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", err)
		return 1
	}
	logger, logErr := platform.NewLogger(paths, os.Stderr)
	if logErr != nil {
		logger.Warn("file logging is unavailable; using console only", "error", logErr)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-desktop: close log:", err)
		}
	}()

	assets, err := newDesktopAssets(invocation.WebRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", err)
		return 1
	}
	streams := newDesktopStreamFactory()
	var wailsApp *application.App
	events := ipc.EmitterFunc(func(ctx context.Context, event ipc.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if wailsApp == nil {
			return ipc.ErrEventsUnavailable
		}
		wailsApp.Event.Emit(string(event.Event), event.Payload)
		return nil
	})
	production, err := production.NewProduction(context.Background(), production.ProductionConfig{
		Config: core.Config{
			Logger:  logger.Logger,
			Events:  events,
			Streams: streams,
			Modules: desktopSmokeModules,
		},
		DataDir:         paths.DataDir,
		Desktop:         true,
		DesktopSmoke:    desktopSmokeEnabled(),
		ForwardPlatform: os.Getenv("NEXTERM_PLATFORM"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", err)
		return 1
	}
	service := &Service{app: production.Application, streams: streams}
	wailsApp = application.New(application.Options{
		Name:        "NexTerm",
		Description: "NexTerm desktop client",
		Logger:      logger.Logger,
		ErrorHandler: func(err error) {
			logger.Error("Wails error", "error", err)
		},
		WarningHandler: func(message string) {
			logger.Warn("Wails warning", "message", message)
		},
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{Handler: desktopSmokeAssetHandler(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	window := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "NexTerm",
		URL:       "/",
		Width:     1480,
		Height:    920,
		MinWidth:  960,
		MinHeight: 600,
		Frameless: true,
	})
	streams.SetWindow(window)
	window.Show()
	if desktopSmokeEnabled() {
		return runDesktopSmoke(wailsApp, window)
	}
	if err := wailsApp.Run(); err != nil {
		logger.Error("desktop stopped", "error", err)
		return 1
	}
	return 0
}
