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
}

func (s *Service) ServiceName() string {
	return "NexTerm"
}

func (s *Service) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	return s.app.Start(ctx)
}

func (s *Service) ServiceShutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(s.app.Shutdown(ctx), s.streams.Close())
}

func (s *Service) Call(ctx context.Context, request ipc.Request) ipc.Response {
	clientID := request.ClientID
	if clientID == "" {
		clientID = "desktop"
	}
	return s.app.Dispatcher.Dispatch(ctx, request, s.app.Environment(clientID))
}
