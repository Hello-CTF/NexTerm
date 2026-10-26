package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/diagcapture"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/platform"
	"github.com/Hello-CTF/NexTerm/internal/pprof"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/update"
	"github.com/Hello-CTF/NexTerm/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
	wailsevents "github.com/wailsapp/wails/v3/pkg/events"
)

// desktopAppID is the product identifier from wails.json. On Linux it is also
// the GTK application id and program name, so the Wayland app_id and the X11
// WM_CLASS match the packaged <desktopAppID>.desktop entry and its icon.
const desktopAppID = "com.nexterm.desktop"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == supervisor.HelperCommand {
		return supervisor.RunHelperCLI(args[1:])
	}
	args, launchLink := extractDeepLink(args)
	invocation, err := core.ParseCLI(args, core.CommandDesktop, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", applog.RedactText(err.Error()))
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
		fmt.Fprintln(os.Stderr, "nexterm-desktop: use the nexterm-server binary for", applog.RedactText(string(invocation.Command)))
		return 2
	}

	paths, err := platform.DesktopPaths(invocation.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", applog.RedactText(err.Error()))
		return 1
	}
	if err := paths.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", applog.RedactText(err.Error()))
		return 1
	}
	logger, logErr := applog.NewLogger(paths, os.Stderr)
	if logErr != nil {
		logger.Warn("file logging is unavailable; using console only", "error", logErr)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-desktop: close log:", applog.RedactText(err.Error()))
		}
	}()
	previousLogger := slog.Default()
	slog.SetDefault(logger.Logger)
	defer slog.SetDefault(previousLogger)
	if capture := diagcapture.StartIfRequested(context.Background(), paths, logger, diagcapture.Info{Mode: "desktop", Args: args}, invocation.DebugCapture); capture != nil {
		defer capture.Stop()
	}
	assets, err := newDesktopAssets(invocation.WebRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", applog.RedactText(err.Error()))
		return 1
	}
	brandIcon := desktopBrandIcon(invocation.WebRoot)
	streams := newDesktopStreamFactory()
	var window *application.WebviewWindow
	var wailsApp *application.App
	events := ipc.EmitterFunc(func(ctx context.Context, event ipc.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if wailsApp == nil {
			return ipc.ErrEventsUnavailable
		}
		wailsApp.Event.Emit(string(event.Event), event.Payload)
		diagcapture.RecordEvent(string(event.Event), diagcapture.PayloadSize(event.Payload))
		return nil
	})
	production, err := production.NewProduction(context.Background(), production.ProductionConfig{
		Config: core.Config{
			Logger:  logger.Logger,
			Events:  events,
			Streams: streams,
		},
		DataDir:         paths.DataDir,
		Desktop:         true,
		ForwardPlatform: os.Getenv("NEXTERM_PLATFORM"),
		RestartFunc: func(_ context.Context, target string) error {
			if err := update.StartDetached(target, args); err != nil {
				return err
			}
			wailsApp.Quit()
			return nil
		},
		ScreenInsets: func(context.Context) (production.ScreenInsets, error) {
			return screenInsets(currentScreen(window)), nil
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", applog.RedactText(err.Error()))
		return showStartupError(paths, err, logger.Logger, brandIcon)
	}
	service := &Service{app: production.Application, streams: streams}
	appOptions := application.Options{
		Name:        "NexTerm",
		Description: "NexTerm desktop client",
		Logger:      logger.WailsLogger(),
		ErrorHandler: func(err error) {
			logger.Error("Wails error", "error", err)
		},
		WarningHandler: func(message string) {
			logger.Warn("Wails warning", "message", message)
		},
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{Handler: assets},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Linux: application.LinuxOptions{ApplicationID: desktopAppID},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: desktopAppID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if window != nil {
					showWindow(window)
				}
				if url := findDeepLink(data.Args); url != "" {
					service.links.deliver(url)
				}
			},
		},
	}
	applyAppIcon(&appOptions, brandIcon)
	wailsApp = application.New(appOptions)
	pprofServer, err := pprof.Start(logger.Logger, invocation.PprofAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-desktop:", applog.RedactText(err.Error()))
		return 1
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = pprofServer.Close(shutdownCtx)
	}()
	service.links.onEmit(func(url string) {
		wailsApp.Event.Emit(deepLinkEvent, url)
	})
	wailsApp.Event.OnApplicationEvent(wailsevents.Common.ApplicationLaunchedWithUrl, func(event *application.ApplicationEvent) {
		url := event.Context().URL()
		if !strings.HasPrefix(url, deepLinkScheme) {
			return
		}
		if window != nil {
			showWindow(window)
		}
		service.links.deliver(url)
	})
	windowOptions := application.WebviewWindowOptions{
		Title:          "NexTerm",
		URL:            "/",
		Width:          1480,
		Height:         920,
		MinWidth:       960,
		MinHeight:      600,
		Frameless:      true,
		EnableFileDrop: true,
	}
	savedWindow, hasSavedWindow := loadWindowState(paths.DataDir)
	if hasSavedWindow {
		windowOptions = applyWindowState(windowOptions, savedWindow, screenBounds(wailsApp))
	}
	window = wailsApp.Window.NewWithOptions(windowOptions)
	trackWindowState(window, paths.DataDir, savedWindow, func(err error) {
		logger.Warn("window state not saved", "error", err)
	})
	setWindowIconWhenReady(window, brandIcon, logger.Logger)
	window.OnWindowEvent(wailsevents.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		emitFilesDropped(wailsApp, event)
	})
	streams.SetWindow(window)
	setupTray(wailsApp, window, brandIcon, production.Services.Store, logger.Logger)
	if launchLink != "" {
		service.links.deliver(launchLink)
	}
	window.Show()
	if err := wailsApp.Run(); err != nil {
		logger.Error("desktop stopped", "error", err)
		return 1
	}
	return 0
}
