package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
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
	coreApp, err := core.New(core.Config{
		Logger: logger.Logger,
		Events: events,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", err)
		return 1
	}
	service := &Service{app: coreApp}
	wailsApp = application.New(application.Options{
		Name:        "NexTerm",
		Description: "NexTerm desktop client",
		Logger:      logger.Logger,
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{Handler: skeletonAssets()},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "NexTerm",
		URL:       "/",
		Width:     1480,
		Height:    920,
		MinWidth:  960,
		MinHeight: 600,
		Frameless: true,
	})
	if err := wailsApp.Run(); err != nil {
		logger.Error("desktop stopped", "error", err)
		return 1
	}
	return 0
}

func skeletonAssets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>NexTerm</title>
  <script>window.__NEXTERM_TRANSPORT__="desktop";</script>
  <script type="module" src="/wails/runtime.js"></script>
</head>
<body>
  <main><h1>NexTerm</h1><p>Go application core is running.</p></main>
</body>
</html>`))
	})
}
