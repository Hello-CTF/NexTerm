package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

func (b *Backend) finishKill(ctx context.Context, id string) error {
	if err := b.waitForRecorder(ctx, id); err != nil {
		return fmt.Errorf("tmux session killed but recorder completion is pending: %w", err)
	}
	return b.removeArtifactsAndMark(id)
}

func (b *Backend) finishMissingKill(ctx context.Context, id string) error {
	present, err := b.artifactsExist(id)
	if err != nil {
		return err
	}
	if !present {
		if !b.wasKilled(id) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil
	}
	if err := b.waitForRecorder(ctx, id); err != nil {
		return fmt.Errorf("tmux session is gone and recorder completion is pending: %w", err)
	}
	return b.removeArtifactsAndMark(id)
}

func (b *Backend) waitForRecorder(ctx context.Context, id string) error {
	waitCtx, cancel := context.WithTimeout(ctx, b.commandTimeout)
	defer cancel()
	for {
		_, err := os.Stat(b.recorderDonePath(id))
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect recorder completion: %v", ErrUnavailable, err)
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (b *Backend) artifactsExist(id string) (bool, error) {
	for _, path := range []string{b.sessionDir(id), b.tombstonePath(id)} {
		_, err := os.Lstat(path)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect durable artifacts for %s: %w", id, err)
		}
	}
	return false, nil
}

func (b *Backend) removeArtifactsAndMark(id string) error {
	if err := b.removeArtifacts(id); err != nil {
		return fmt.Errorf("tmux session killed but durable artifacts remain: %w", err)
	}
	b.killedMu.Lock()
	b.killed[id] = struct{}{}
	b.killedMu.Unlock()
	return nil
}

func (b *Backend) wasKilled(id string) bool {
	b.killedMu.Lock()
	_, ok := b.killed[id]
	b.killedMu.Unlock()
	return ok
}
