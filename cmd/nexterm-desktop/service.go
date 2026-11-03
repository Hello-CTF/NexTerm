package main

import (
	"context"
	"errors"
	"time"

	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type Service struct {
	app     *core.Application
	streams *desktopStreamFactory
	links   deepLinkRelay
}

func (s *Service) ServiceName() string {
	return "NexTerm"
}

func (s *Service) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	return s.app.Start(ctx)
}

// serviceShutdownTimeout is the component deadline, not a hard cap: components that ignore the context still get a
// grace period after it. Wails runs ServiceShutdown on the main UI thread, so a stuck stream dispatch cannot be
// waited out; streams are closed first so nothing else dispatches.
// Remaining limits: a dispatch goroutine abandoned by Close lives until the window closes; a component that
// ignores its context still waits for the deadline plus grace. The real fix is running Shutdown off the UI thread.
const serviceShutdownTimeout = 5 * time.Second

func (s *Service) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	streamsErr := s.streams.Close()
	appErr := s.app.Shutdown(ctx)
	return errors.Join(appErr, streamsErr)
}

func (s *Service) Call(ctx context.Context, request ipc.Request) ipc.Response {
	clientID := request.ClientID
	if clientID == "" {
		clientID = "desktop"
	}
	return s.app.Dispatcher.Dispatch(ctx, request, s.app.Environment(clientID))
}

func (s *Service) ConsumeDeepLinks() []string {
	return s.links.consume()
}
