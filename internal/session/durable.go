package session

import (
	"context"
	"fmt"
	"io"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type tmuxDurableProvider struct {
	backend *durable.Backend
}

func NewDurableProvider(backend *durable.Backend) base.DurableProvider {
	if backend == nil {
		return nil
	}
	return &tmuxDurableProvider{backend: backend}
}

func (p *tmuxDurableProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.backend.Create(ctx, durable.CreateOptions{
		ID: options.ID, Command: options.Command, Dir: options.Dir, Env: options.Env,
		Cols: options.Cols, Rows: options.Rows,
	})
	if attachment == nil {
		return nil, err
	}
	return &tmuxDurableAttachment{backend: p.backend, session: attachment}, err
}

func (p *tmuxDurableProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	attachment, err := p.backend.Attach(ctx, id)
	if attachment == nil {
		return nil, err
	}
	return &tmuxDurableAttachment{backend: p.backend, session: attachment}, err
}

type tmuxDurableAttachment struct {
	backend *durable.Backend
	session *durable.Session
}

type durableVersionFloor interface {
	DurableVersions() (eventVersion, gridRevision uint64, err error)
	PersistDurableVersions(eventVersion, gridRevision uint64) error
}

type durableGridSource interface {
	DurableGrid() (cols, rows uint32, ok bool)
}

type durableTranscriptOffsetSource interface {
	DurableTranscriptCatchUpBytes(tabID string) int64
	PersistDurableTranscriptOffset(tabID string, offset int64)
}

func (a *tmuxDurableAttachment) DurableVersions() (eventVersion, gridRevision uint64, err error) {
	return a.session.Versions()
}

func (a *tmuxDurableAttachment) PersistDurableVersions(eventVersion, gridRevision uint64) error {
	return a.session.PersistVersions(eventVersion, gridRevision)
}

func (a *tmuxDurableAttachment) DurableGrid() (cols, rows uint32, ok bool) {
	info := a.session.Info()
	if info.Cols == 0 || info.Rows == 0 {
		return 0, 0, false
	}
	return info.Cols, info.Rows, true
}

func (a *tmuxDurableAttachment) Read(buffer []byte) (int, error) {
	return a.session.Read(buffer)
}

func (a *tmuxDurableAttachment) Write(buffer []byte) (int, error) {
	return a.session.Write(buffer)
}

func (a *tmuxDurableAttachment) Close() error {
	return a.session.Detach()
}

func (a *tmuxDurableAttachment) Kill(ctx context.Context) error {
	return a.session.Kill(ctx)
}

func (a *tmuxDurableAttachment) Stderr() io.Reader { return nil }

func (a *tmuxDurableAttachment) Resize(ctx context.Context, cols, rows uint32) error {
	return a.session.Resize(ctx, cols, rows)
}

func (a *tmuxDurableAttachment) Wait(ctx context.Context) error {
	expected := a.session.Info()
	infos, err := a.backend.List(ctx)
	if err != nil {
		return err
	}
	for _, current := range infos {
		if current.ID != expected.ID {
			continue
		}
		if !sameDurableIdentity(expected, current) {
			return fmt.Errorf("%w: %s", durable.ErrIdentity, expected.ID)
		}
		if !current.Dead {
			return fmt.Errorf("%w: durable output ended before process exit", durable.ErrUnavailable)
		}
		if current.ExitCode != nil {
			return &base.ExitError{Code: *current.ExitCode, Signal: current.Signal}
		}
		return nil
	}
	return fmt.Errorf("%w: %s", durable.ErrNotFound, expected.ID)
}

func (a *tmuxDurableAttachment) CloseWrite() error  { return base.ErrUnsupported }
func (a *tmuxDurableAttachment) ID() string         { return a.session.Info().ID }
func (a *tmuxDurableAttachment) Generation() uint64 { return 0 }

func sameDurableIdentity(left, right durable.Info) bool {
	return left.ID == right.ID &&
		left.SessionID == right.SessionID &&
		left.PaneID == right.PaneID &&
		left.PID == right.PID &&
		left.CreatedAt.Equal(right.CreatedAt)
}
