package services

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

type Executor interface {
	Execute(context.Context, string, execbatch.Request) (execbatch.Batch, error)
}

// Which commands a host understands.
const (
	PlatformSystemd = "systemd"
	PlatformWindows = "windows"
)

// PlatformResolver reports PlatformWindows for a session whose exec channel speaks PowerShell, and anything
// else for hosts that are managed through systemctl. It also authorizes ownerID for the session.
type PlatformResolver func(ctx context.Context, ownerID, sessionID string) (string, error)

type Service struct {
	executor Executor
	platform PlatformResolver
}

func New(executor Executor) *Service {
	return &Service{executor: executor}
}

// WithPlatformResolver lets the service address Windows hosts. Without it every host is treated as systemd.
func (s *Service) WithPlatformResolver(resolver PlatformResolver) *Service {
	s.platform = resolver
	return s
}

// platformFor returns the one platform shared by all sessions. A batch runs a single command on every
// target, so a mix of Windows and other hosts is refused instead of being split, which would give each part
// its own production confirmation.
func (s *Service) platformFor(ctx context.Context, ownerID string, sessionIDs []string) (string, error) {
	if s.platform == nil {
		return PlatformSystemd, nil
	}
	platform := ""
	for _, id := range sessionIDs {
		got, err := s.platform(ctx, ownerID, id)
		if err != nil {
			return "", err
		}
		if got != PlatformWindows {
			got = PlatformSystemd
		}
		if platform != "" && got != platform {
			return "", fmt.Errorf("%w: Windows and Linux hosts cannot share one services batch; operate on them separately", ErrInvalid)
		}
		platform = got
	}
	if platform == "" {
		platform = PlatformSystemd
	}
	return platform, nil
}

func (s *Service) List(ctx context.Context, ownerID string, input ListRequest) (ServiceOperationBatch, error) {
	platform, err := s.platformFor(ctx, ownerID, input.SessionIDs)
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	command := SystemdListCommand(input.Sudo)
	if platform == PlatformWindows {
		command = WindowsListCommand()
		ctx = execbatch.WithPowerShellTargets(ctx)
	}
	batch, err := s.execute(ctx, ownerID, execbatch.Request{
		BatchID: input.BatchID, SessionIDs: input.SessionIDs, Command: command,
		TimeoutMS: input.TimeoutMS, MaxConcurrent: input.MaxConcurrent, ProductionConfirmation: input.ProductionConfirmation,
	})
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	return serviceBatch(batch, platform, true), nil
}

func (s *Service) Action(ctx context.Context, ownerID string, input ActionRequest) (ServiceOperationBatch, error) {
	platform, err := s.platformFor(ctx, ownerID, input.SessionIDs)
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	var command string
	if platform == PlatformWindows {
		command, err = WindowsActionCommand(input.Action, input.Unit)
		ctx = execbatch.WithPowerShellTargets(ctx)
	} else {
		command, err = SystemdActionCommand(input.Action, input.Unit, input.Sudo)
	}
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	batch, err := s.execute(ctx, ownerID, execbatch.Request{
		BatchID: input.BatchID, SessionIDs: input.SessionIDs, Command: command,
		TimeoutMS: input.TimeoutMS, MaxConcurrent: input.MaxConcurrent, ProductionConfirmation: input.ProductionConfirmation,
	})
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	return serviceBatch(batch, platform, false), nil
}

func (s *Service) Find(ctx context.Context, ownerID string, input FindRequest) (FinderBatch, error) {
	matcher, err := newFinderMatcher(input.Query)
	if err != nil {
		return FinderBatch{}, err
	}
	batch, err := s.execute(ctx, ownerID, execbatch.Request{
		BatchID: input.BatchID, SessionIDs: input.SessionIDs, Command: FinderCommand(input.Sudo),
		TimeoutMS: input.TimeoutMS, MaxConcurrent: input.MaxConcurrent, ProductionConfirmation: input.ProductionConfirmation,
	})
	if err != nil {
		return FinderBatch{}, err
	}
	hosts := make([]FinderHostResult, len(batch.Results))
	for index, result := range batch.Results {
		hosts[index] = finderHostResult(result, matcher)
	}
	return FinderBatch{BatchID: batch.BatchID, Command: batch.Command, Hosts: hosts}, nil
}

func (s *Service) Kill(ctx context.Context, ownerID string, input KillRequest) (ServiceOperationBatch, error) {
	command, err := KillCommand(input.PID, input.Sudo)
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	batch, err := s.execute(ctx, ownerID, execbatch.Request{
		BatchID: input.BatchID, SessionIDs: []string{input.SessionID}, Command: command,
		TimeoutMS: input.TimeoutMS, ProductionConfirmation: input.ProductionConfirmation,
	})
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	return serviceBatch(batch, PlatformSystemd, false), nil
}

func (s *Service) execute(ctx context.Context, ownerID string, request execbatch.Request) (execbatch.Batch, error) {
	if s.executor == nil {
		return execbatch.Batch{}, ErrUnsupported
	}
	batch, err := s.executor.Execute(ctx, ownerID, request)
	if err != nil {
		return execbatch.Batch{}, fmt.Errorf("execute host services batch: %w", err)
	}
	return batch, nil
}

func serviceBatch(batch execbatch.Batch, platform string, list bool) ServiceOperationBatch {
	hosts := make([]ServiceHostResult, len(batch.Results))
	for index, result := range batch.Results {
		if platform == PlatformWindows {
			hosts[index] = windowsHostResult(result, list)
		} else {
			hosts[index] = systemdHostResult(result, list)
		}
	}
	return ServiceOperationBatch{BatchID: batch.BatchID, Command: batch.Command, Hosts: hosts}
}
