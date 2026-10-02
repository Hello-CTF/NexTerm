package main

import (
	"context"
	"time"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type Service struct {
	app *core.Application
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
	return s.app.Shutdown(ctx)
}

func (s *Service) Call(ctx context.Context, request ipc.Request) ipc.Response {
	return s.app.Dispatcher.Dispatch(ctx, request, s.app.Environment("desktop"))
}
