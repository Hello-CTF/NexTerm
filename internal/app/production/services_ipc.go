package production

import (
	"context"
	"errors"
	"runtime"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	hostservices "github.com/Hello-CTF/NexTerm/internal/services"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

type servicesIPCService struct {
	services *hostservices.Service
}

// registerServiceCommands serves the host services panel. With a session manager, Windows hosts are managed
// through PowerShell; without one every host is treated as systemd.
func registerServiceCommands(dispatcher *ipc.Dispatcher, batches *execbatch.Service, sessions *session.Manager) error {
	var executor hostservices.Executor
	if batches != nil {
		executor = batches
	}
	service := hostservices.New(executor)
	if sessions != nil {
		service.WithPlatformResolver(servicesPlatformResolver(sessions))
	}
	return (&servicesIPCService{services: service}).register(dispatcher)
}

// servicesPlatformResolver reuses the Windows detection of the process and port views: WinRM, a local session
// on Windows, a Windows device agent, or an SSH asset configured with a Windows shell.
func servicesPlatformResolver(sessions *session.Manager) hostservices.PlatformResolver {
	return func(ctx context.Context, _ string, sessionID string) (string, error) {
		connected, err := sessions.SessionFor(ctx, sessionID)
		if err != nil {
			return "", session.IPCError(err)
		}
		shell := hostPortsShellForGOOS(connected.Asset(), runtime.GOOS)
		if connected.Asset().Kind == session.KindDevice {
			transport, err := sessions.TransportFor(ctx, sessionID)
			if err != nil {
				return "", session.IPCError(err)
			}
			if deviceTransport, ok := transport.(*device.Transport); ok && deviceTransport.Platform() == "windows" {
				shell = hostports.ShellPowerShell
			}
		}
		if shell == hostports.ShellPowerShell {
			return hostservices.PlatformWindows, nil
		}
		return hostservices.PlatformSystemd, nil
	}
}

func (s *servicesIPCService) register(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "services_list", func(ctx context.Context, _ *ipc.Call, input hostservices.ListRequest) (hostservices.ServiceOperationBatch, error) {
				result, err := s.services.List(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
				if err != nil {
					return hostservices.ServiceOperationBatch{}, servicesIPCError(err)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "services_action", func(ctx context.Context, _ *ipc.Call, input hostservices.ActionRequest) (hostservices.ServiceOperationBatch, error) {
				result, err := s.services.Action(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
				if err != nil {
					return hostservices.ServiceOperationBatch{}, servicesIPCError(err)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "services_find", func(ctx context.Context, _ *ipc.Call, input hostservices.FindRequest) (hostservices.FinderBatch, error) {
				result, err := s.services.Find(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
				if err != nil {
					return hostservices.FinderBatch{}, servicesIPCError(err)
				}
				return result, nil
			})
		},
		func() error {
			return ipc.Register(dispatcher, "services_kill", func(ctx context.Context, _ *ipc.Call, input hostservices.KillRequest) (hostservices.ServiceOperationBatch, error) {
				result, err := s.services.Kill(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
				if err != nil {
					return hostservices.ServiceOperationBatch{}, servicesIPCError(err)
				}
				return result, nil
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

func servicesIPCError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hostservices.ErrInvalid):
		return ipc.BadParam(err)
	case errors.Is(err, hostservices.ErrUnsupported):
		return ipc.WrapError(ipc.CodeUnsupported, err.Error(), err)
	default:
		return execBatchIPCError(err)
	}
}
