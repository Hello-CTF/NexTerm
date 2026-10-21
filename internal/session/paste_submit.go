package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// WritePasteSubmitFor writes text as literal terminal input and, when submit
// is set, the Enter key after the given delay under one and the same write
// lock, so user input can never interleave between the paste and the
// submission. The literal-newline guard is enforced inside that lock: when
// the in-lock bracketed-paste mode is off, content whose newlines would map
// to carriage returns is rejected with ErrPasteGuarded before any byte is
// written. Cancelling ctx during the delay stops the sequence before the
// Enter and reports the context error: the paste already landed, so the
// caller must not retry blindly.
func (m *Manager) WritePasteSubmitFor(ctx context.Context, tabID, text string, submit bool, delay time.Duration) error {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return err
	}
	release, err := tab.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := tab.lockFeed(ctx); err != nil {
		return err
	}
	core, ok := tab.terminal.(interface{ BracketedPasteMode() (bool, uint64) })
	if !ok {
		tab.unlockFeed()
		return ErrUnsupported
	}
	bracketed, generation := core.BracketedPasteMode()
	encoded := terminal.EncodePaste(text, bracketed)
	tab.unlockFeed()
	if !bracketed && strings.ContainsAny(text, "\r\n") {
		return ErrPasteGuarded
	}
	if err := m.writeInternalLocked(ctx, tab, encoded); err != nil {
		return err
	}
	if err := tab.lockFeed(ctx); err != nil {
		return errors.Join(ErrPasteVerification, err)
	}
	_, currentGeneration := core.BracketedPasteMode()
	tab.unlockFeed()
	if currentGeneration != generation {
		return ErrPasteModeChanged
	}
	if !submit {
		return nil
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		case <-tab.ctx.Done():
			return ErrTabClosed
		}
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
	}
	return m.writeInternalLocked(ctx, tab, []byte("\r"))
}
