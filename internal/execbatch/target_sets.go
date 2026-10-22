package execbatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TargetSetStore is the persistence contract for per-owner target sets. The
// first version of target sets is local-only: the contract carries no sync
// fields, so deletes never produce tombstones.
type TargetSetStore interface {
	ExecTargetSetCreate(ctx context.Context, name string, sessionIDs []string) (store.ExecTargetSet, error)
	ExecTargetSetList(ctx context.Context) ([]store.ExecTargetSet, error)
	ExecTargetSetGet(ctx context.Context, id string) (store.ExecTargetSet, error)
	ExecTargetSetUpdate(ctx context.Context, id, name string, sessionIDs []string) error
	ExecTargetSetDelete(ctx context.Context, id string) error
}

// TargetSetService exposes per-owner target-set CRUD and the picker list on
// top of the store. It is a service primitive, not the user-facing feature:
// the IPC and UI picker entry points are wired separately. Owner isolation
// comes from the ownerID parameter attached to the store context; unlike
// audit/history writes, every call stays cancelable by the caller's context.
type TargetSetService struct {
	store TargetSetStore
}

func NewTargetSetService(store TargetSetStore) *TargetSetService {
	return &TargetSetService{store: store}
}

func (s *TargetSetService) Create(ctx context.Context, ownerID, name string, sessionIDs []string) (store.ExecTargetSet, error) {
	if err := ctx.Err(); err != nil {
		return store.ExecTargetSet{}, err
	}
	if s.store == nil {
		return store.ExecTargetSet{}, errTargetSetStoreMissing
	}
	trimmed, err := validateTargetSetNameInput(name)
	if err != nil {
		return store.ExecTargetSet{}, err
	}
	if err := validateTargetSetSessionsInput(sessionIDs); err != nil {
		return store.ExecTargetSet{}, err
	}
	return s.store.ExecTargetSetCreate(targetSetContext(ctx, ownerID), trimmed, sessionIDs)
}

func (s *TargetSetService) List(ctx context.Context, ownerID string) ([]store.ExecTargetSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, errTargetSetStoreMissing
	}
	sets, err := s.store.ExecTargetSetList(targetSetContext(ctx, ownerID))
	if err != nil {
		return nil, err
	}
	if sets == nil {
		sets = []store.ExecTargetSet{}
	}
	return sets, nil
}

func (s *TargetSetService) Get(ctx context.Context, ownerID, id string) (store.ExecTargetSet, error) {
	if err := ctx.Err(); err != nil {
		return store.ExecTargetSet{}, err
	}
	if s.store == nil {
		return store.ExecTargetSet{}, errTargetSetStoreMissing
	}
	if err := validateTargetSetIDInput(id); err != nil {
		return store.ExecTargetSet{}, err
	}
	return s.store.ExecTargetSetGet(targetSetContext(ctx, ownerID), id)
}

func (s *TargetSetService) Update(ctx context.Context, ownerID, id, name string, sessionIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.store == nil {
		return errTargetSetStoreMissing
	}
	if err := validateTargetSetIDInput(id); err != nil {
		return err
	}
	trimmed, err := validateTargetSetNameInput(name)
	if err != nil {
		return err
	}
	if err := validateTargetSetSessionsInput(sessionIDs); err != nil {
		return err
	}
	return s.store.ExecTargetSetUpdate(targetSetContext(ctx, ownerID), id, trimmed, sessionIDs)
}

func (s *TargetSetService) Delete(ctx context.Context, ownerID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.store == nil {
		return errTargetSetStoreMissing
	}
	if err := validateTargetSetIDInput(id); err != nil {
		return err
	}
	return s.store.ExecTargetSetDelete(targetSetContext(ctx, ownerID), id)
}

var errTargetSetStoreMissing = errors.New("exec batch target set store is not configured")

// targetSetContext attaches the owner for store-side scoping while keeping
// the caller's cancellation and deadline intact.
func targetSetContext(ctx context.Context, ownerID string) context.Context {
	return ipc.WithUserID(ctx, ownerID)
}

func validateTargetSetNameInput(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: target set name is required", ErrInvalid)
	}
	if len(trimmed) > store.ExecTargetSetMaxNameBytes {
		return "", fmt.Errorf("%w: target set name exceeds %d bytes", ErrInvalid, store.ExecTargetSetMaxNameBytes)
	}
	return trimmed, nil
}

func validateTargetSetSessionsInput(sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return fmt.Errorf("%w: target set requires at least one session", ErrInvalid)
	}
	if len(sessionIDs) > store.ExecTargetSetMaxTargets {
		return fmt.Errorf("%w: target set exceeds %d targets", ErrInvalid, store.ExecTargetSetMaxTargets)
	}
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == "" || len(sessionID) > store.ExecTargetSetMaxSessionIDLen || strings.TrimSpace(sessionID) != sessionID {
			return fmt.Errorf("%w: invalid target set session id", ErrInvalid)
		}
		if _, exists := seen[sessionID]; exists {
			return fmt.Errorf("%w: duplicate target set session %q", ErrInvalid, sessionID)
		}
		seen[sessionID] = struct{}{}
	}
	return nil
}

func validateTargetSetIDInput(id string) error {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
		return fmt.Errorf("%w: invalid target set reference", ErrInvalid)
	}
	return nil
}
