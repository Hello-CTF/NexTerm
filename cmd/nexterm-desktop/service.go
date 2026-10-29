package main

import (
	"context"
	"errors"
	"fmt"
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

// serviceShutdownTimeout bounds the whole shutdown: Wails runs ServiceShutdown on the main UI thread, so every second
// spent here is a frozen window.
const serviceShutdownTimeout = 5 * time.Second

func (s *Service) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	appErr := s.app.Shutdown(ctx)
	closed := make(chan error, 1)
	go func() { closed <- s.streams.Close() }()
	select {
	case streamsErr := <-closed:
		return errors.Join(appErr, streamsErr)
	case <-ctx.Done():
		return errors.Join(appErr, fmt.Errorf("close streams: %w", ctx.Err()))
	}
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
