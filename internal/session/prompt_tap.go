package session

import (
	"context"
	"fmt"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// PromptTapRequest answers the detected prompt the user saw, identified by
// the helper generation, prompt epoch and fingerprint of its attention
// event, with one of its options.
type PromptTapRequest struct {
	Generation  string
	Epoch       uint64
	Fingerprint string
	Option      int
}

// TapPromptFor answers a durable tab's detected prompt as client's input.
// The helper inspects the screen again and writes the option's keys only
// when the prompt is still the one identified by the request and tappable;
// the tap consumes the prompt epoch, so a repeated tap fails. A stale tap
// fails with base.ErrStalePrompt, writes nothing and counts as no input.
// Tabs outside a helper cannot read the terminal foreground, so their
// prompts are never tappable (ErrUnsupported).
func (m *Manager) TapPromptFor(ctx context.Context, tabID, client string, request PromptTapRequest) error {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return err
	}
	if request.Generation == "" || request.Fingerprint == "" || request.Option < 0 {
		return fmt.Errorf("%w: prompt tap needs a generation, a fingerprint and an option", ErrInvalidOptions)
	}
	client = clientID(client)
	// The write gate orders the keys after input this process already
	// queued: input waiting for the gate is sent first.
	release, err := tab.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	tab.inputMu.Lock()
	queued := tab.inputQueue
	tab.inputQueue = nil
	tab.inputMu.Unlock()
	if len(queued) > 0 {
		m.writeInputBatch(ctx, tab, queued)
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if tab.spectatorOnlyLocked(client) {
		tab.mu.Unlock()
		return ErrForbidden
	}
	tapper, ok := tab.durable.(base.DurablePromptTapper)
	generation := tab.generation
	exited := tab.exited
	tab.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: prompts of this terminal cannot be answered by tap", ErrUnsupported)
	}
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if exited {
		return ErrTabClosed
	}
	sent, err := tapper.TapDurablePrompt(ctx, base.PromptTap{
		Generation: request.Generation, Epoch: request.Epoch, Fingerprint: request.Fingerprint, Option: request.Option,
	})
	if err != nil {
		return err
	}
	// Only an accepted tap is this client's input. The input owner is
	// announced after the helper queued the keys: the tap is validated and
	// written as one step there, so a refused tap never moves the owner.
	m.notifyUserInput(tabID)
	tab.mu.Lock()
	ownerChanged, controlChanged := false, false
	var control ControlEvent
	if !tab.closed {
		ownerChanged = tab.noteInputClientLocked(client)
		controlChanged = tab.noteInputLocked(client, m.now())
		if controlChanged {
			control = tab.controlEventLocked()
		}
	}
	tab.mu.Unlock()
	if controlChanged {
		m.emit(context.Background(), TopicTerminalControl, control)
	}
	if ownerChanged {
		m.syncInputOwner(ctx, tab)
	}
	m.transcriptInput(ctx, tab, []byte(sent))
	return nil
}
