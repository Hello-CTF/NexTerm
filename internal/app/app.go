package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

type VaultStatusFunc func(context.Context) (any, error)

type Module struct {
	Name             string
	RegisterCommands func(*ipc.Dispatcher) error
	Component        Component
}

type Config struct {
	Name            string
	Version         string
	GOOS            string
	Events          ipc.Emitter
	Streams         ipc.StreamFactory
	VaultStatus     VaultStatusFunc
	RetentionStatus RetentionStatusFunc
	Logger          *slog.Logger
	Modules         []Module
}

type Application struct {
	Dispatcher *ipc.Dispatcher

	lifecycle       Lifecycle
	environment     ipc.Environment
	name            string
	version         string
	goos            string
	vaultStatus     VaultStatusFunc
	retentionStatus RetentionStatusFunc
	logger          *slog.Logger
}

type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Vault   any    `json:"vault"`
}

func New(config Config) (*Application, error) {
	if config.Name == "" {
		config.Name = "NexTerm"
	}
	if config.Version == "" {
		config.Version = version.Version
	}
	if config.GOOS == "" {
		config.GOOS = runtime.GOOS
	}
	if config.Streams == nil {
		config.Streams = ipc.StreamFactoryFuncs{}
	}
	if config.Events == nil {
		config.Events = ipc.EmitterFunc(func(context.Context, ipc.Event) error {
			return ipc.ErrEventsUnavailable
		})
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	a := &Application{
		Dispatcher:      ipc.NewDispatcher(),
		environment:     ipc.Environment{Events: config.Events, Streams: config.Streams},
		name:            config.Name,
		version:         config.Version,
		goos:            config.GOOS,
		vaultStatus:     config.VaultStatus,
		retentionStatus: config.RetentionStatus,
		logger:          config.Logger,
	}
	if err := ipc.Register(a.Dispatcher, "app_info", func(ctx context.Context, _ *ipc.Call, _ struct{}) (Info, error) {
		var vault any
		if a.vaultStatus != nil {
			var err error
			vault, err = a.vaultStatus(ctx)
			if err != nil {
				return Info{}, err
			}
		}
		return Info{Name: a.name, Version: a.version, Vault: vault}, nil
	}); err != nil {
		return nil, err
	}
	if err := ipc.Register(a.Dispatcher, "app_platform", func(_ context.Context, _ *ipc.Call, _ struct{}) (string, error) {
		return a.goos, nil
	}); err != nil {
		return nil, err
	}

	for _, module := range config.Modules {
		if module.RegisterCommands != nil {
			if err := module.RegisterCommands(a.Dispatcher); err != nil {
				return nil, fmt.Errorf("register module %s: %w", module.Name, err)
			}
		}
		if module.Component != nil {
			if err := a.AddComponent(module.Name, module.Component); err != nil {
				return nil, err
			}
		}
	}
	return a, nil
}

func (a *Application) Environment(clientID string) ipc.Environment {
	environment := a.environment
	environment.ClientID = clientID
	return environment
}

func (a *Application) AddComponent(name string, component Component) error {
	return a.lifecycle.Add(name, component)
}

func (a *Application) Start(ctx context.Context) error {
	return a.lifecycle.Start(ctx)
}

func (a *Application) Shutdown(ctx context.Context) error {
	return a.lifecycle.Shutdown(ctx)
}
