package production

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	hostservices "github.com/Hello-CTF/NexTerm/internal/services"
)

type servicesIPCService struct {
	services *hostservices.Service
}

func registerServiceCommands(dispatcher *ipc.Dispatcher, batches *execbatch.Service) error {
	var executor hostservices.Executor
	if batches != nil {
		executor = batches
	}
	return (&servicesIPCService{services: hostservices.New(executor)}).register(dispatcher)
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
