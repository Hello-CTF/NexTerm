package services

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

type Executor interface {
	Execute(context.Context, string, execbatch.Request) (execbatch.Batch, error)
}

type Service struct {
	executor Executor
}

func New(executor Executor) *Service {
	return &Service{executor: executor}
}

func (s *Service) List(ctx context.Context, ownerID string, input ListRequest) (ServiceOperationBatch, error) {
	batch, err := s.execute(ctx, ownerID, execbatch.Request{
		BatchID: input.BatchID, SessionIDs: input.SessionIDs, Command: SystemdListCommand(input.Sudo),
		TimeoutMS: input.TimeoutMS, MaxConcurrent: input.MaxConcurrent, ProductionConfirmation: input.ProductionConfirmation,
	})
	if err != nil {
		return ServiceOperationBatch{}, err
	}
	return serviceBatch(batch, true), nil
}

func (s *Service) Action(ctx context.Context, ownerID string, input ActionRequest) (ServiceOperationBatch, error) {
	command, err := SystemdActionCommand(input.Action, input.Unit, input.Sudo)
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
	return serviceBatch(batch, false), nil
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
	return serviceBatch(batch, false), nil
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

func serviceBatch(batch execbatch.Batch, list bool) ServiceOperationBatch {
	hosts := make([]ServiceHostResult, len(batch.Results))
	for index, result := range batch.Results {
		hosts[index] = systemdHostResult(result, list)
	}
	return ServiceOperationBatch{BatchID: batch.BatchID, Command: batch.Command, Hosts: hosts}
}
