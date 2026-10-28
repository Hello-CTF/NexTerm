package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

type Attachment struct {
	session *Session

	replayMu sync.Mutex
	replay   *historyReader
	// nextSeq is the absolute offset of the next byte Read returns. It jumps
	// forward when the reader fell behind eviction.
	nextSeq uint64
	// replayEnd is the recorded size at attach: bytes before it are replay.
	replayEnd uint64
	result    ReplayInfo
	// gaps counts the forward jumps over evicted output.
	gaps uint64

	notify chan struct{}
	// stateNotify is signalled when the session's attention state or
	// events change.
	stateNotify chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc

	closeOnce sync.Once
	closeErr  error
}

func (a *Attachment) ID() string {
	return a.session.ID()
}

func (a *Attachment) Info() Info {
	return a.session.Info()
}

// Identity returns the session identity with the generation of the helper
// that serves this attachment.
func (a *Attachment) Identity() Identity {
	identity := a.session.Identity()
	identity.Generation = a.session.supervisor.generation
	return identity
}

func (a *Attachment) NextSeq() uint64 {
	a.replayMu.Lock()
	defer a.replayMu.Unlock()
	return a.nextSeq
}

// Replay reports where this attachment started.
func (a *Attachment) Replay() ReplayInfo {
	return a.result
}

func (a *Attachment) Read(p []byte) (int, error) {
	_, count, err := a.readChunk(p)
	return count, err
}

// readChunk reads like Read and also returns the absolute offset of the
// returned bytes. When the output at the reader position was evicted, the
// reader jumps to the first safe boundary after the new history base; the
// returned offset then lies beyond the previous end, and Gaps counts it.
func (a *Attachment) readChunk(p []byte) (uint64, int, error) {
	if len(p) == 0 {
		return a.NextSeq(), 0, nil
	}
	for {
		if a.closed() {
			return 0, 0, ErrClosed
		}
		a.replayMu.Lock()
		replay := a.replay
		a.replayMu.Unlock()
		if replay == nil {
			return 0, 0, ErrClosed
		}
		seq := replay.pos
		count, err := replay.read(p)
		if count > 0 {
			a.replayMu.Lock()
			a.nextSeq = seq + uint64(count)
			a.replayMu.Unlock()
			return seq, count, nil
		}
		if errors.Is(err, errHistoryEvicted) {
			if skipErr := a.skipEvicted(replay); skipErr != nil {
				return 0, 0, skipErr
			}
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		dead, recErr := a.session.status()
		if recErr != nil {
			return 0, 0, recErr
		}
		if dead {
			return 0, 0, io.EOF
		}
		select {
		case <-a.notify:
		case <-a.session.deadCh:
		case <-a.ctx.Done():
			return 0, 0, ErrClosed
		}
	}
}

func (a *Attachment) skipEvicted(replay *historyReader) error {
	for attempt := 0; attempt < 4; attempt++ {
		base, _ := a.session.history.bounds()
		start, err := a.session.history.safeStartFrom(base)
		if errors.Is(err, errHistoryEvicted) {
			continue
		}
		if err != nil {
			return err
		}
		replay.pos = start
		a.replayMu.Lock()
		a.nextSeq = start
		a.gaps++
		a.replayMu.Unlock()
		return nil
	}
	return fmt.Errorf("%w: recorded output is evicted faster than it can be streamed", ErrUnavailable)
}

// Gaps returns how many times the attachment skipped evicted output.
func (a *Attachment) Gaps() uint64 {
	a.replayMu.Lock()
	defer a.replayMu.Unlock()
	return a.gaps
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

// writeBlocking queues p for the session, waiting while the input queue is full
// and giving up when the attachment is detached.
func (a *Attachment) writeBlocking(p []byte) (int, error) {
	if a.closed() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	return a.session.writeInputCtx(a.ctx, p, true)
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
		replay := a.replay
		a.replay = nil
		a.replayMu.Unlock()
		if replay != nil {
			replay.close()
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
