package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

type Attachment struct {
	session *Session

	replayMu sync.Mutex
	replay   *os.File
	nextSeq  uint64

	notify chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
	closeErr  error
}

func (a *Attachment) ID() string {
	return a.session.ID()
}

func (a *Attachment) Info() Info {
	return a.session.Info()
}

func (a *Attachment) Identity() Identity {
	return a.session.Identity()
}

func (a *Attachment) NextSeq() uint64 {
	a.replayMu.Lock()
	defer a.replayMu.Unlock()
	return a.nextSeq
}

func (a *Attachment) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if a.closed() {
			return 0, ErrClosed
		}
		a.replayMu.Lock()
		replay := a.replay
		a.replayMu.Unlock()
		if replay != nil {
			count, err := replay.Read(p)
			if count > 0 {
				a.replayMu.Lock()
				a.nextSeq += uint64(count)
				a.replayMu.Unlock()
				return count, nil
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return 0, fmt.Errorf("%w: read supervisor recording: %v", ErrUnavailable, err)
			}
		}
		dead, recErr := a.session.status()
		if recErr != nil {
			return 0, recErr
		}
		if dead {
			return 0, io.EOF
		}
		select {
		case <-a.notify:
		case <-a.session.deadCh:
		case <-a.ctx.Done():
			return 0, ErrClosed
		}
	}
}

func (a *Attachment) Write(p []byte) (int, error) {
	if a.closed() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	return a.session.Write(p)
}

func (a *Attachment) Resize(ctx context.Context, cols, rows uint32) error {
	if a.closed() {
		return ErrClosed
	}
	return a.session.Resize(ctx, cols, rows)
}

func (a *Attachment) Wait(ctx context.Context) error {
	if a.closed() {
		return ErrClosed
	}
	return a.session.Wait(ctx)
}

func (a *Attachment) Kill(ctx context.Context) error {
	identity := a.identity()
	if err := a.session.supervisor.kill(ctx, a.session.ID(), &identity); err != nil {
		return err
	}
	return a.Detach()
}

func (a *Attachment) Versions() (eventVersion, gridRevision uint64, err error) {
	return a.session.Versions()
}

func (a *Attachment) PersistVersions(eventVersion, gridRevision uint64) error {
	return a.session.PersistVersions(eventVersion, gridRevision)
}

func (a *Attachment) Detach() error {
	a.closeOnce.Do(func() {
		a.cancel()
		a.session.removeAttachment(a)
		a.replayMu.Lock()
		defer a.replayMu.Unlock()
		if a.replay != nil {
			a.closeErr = a.replay.Close()
			if errors.Is(a.closeErr, os.ErrClosed) {
				a.closeErr = nil
			}
			a.replay = nil
		}
	})
	return a.closeErr
}

func (a *Attachment) Close() error {
	return a.Detach()
}

func (a *Attachment) closed() bool {
	select {
	case <-a.ctx.Done():
		return true
	default:
		return false
	}
}

func (a *Attachment) identity() Identity {
	return a.session.Identity()
}
