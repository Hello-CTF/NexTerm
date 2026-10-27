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
// fails with base.ErrStalePrompt and writes nothing. Tabs outside a helper
// cannot read the terminal foreground, so their prompts are never tappable
// (ErrUnsupported).
func (m *Manager) TapPromptFor(ctx context.Context, tabID, client string, request PromptTapRequest) error {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return err
	}
	if request.Generation == "" || request.Fingerprint == "" || request.Option < 0 {
		return fmt.Errorf("%w: prompt tap needs a generation, a fingerprint and an option", ErrInvalidOptions)
	}
	m.notifyUserInput(tabID)
	// The write gate orders the keys after input this process already sent.
	release, err := tab.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
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
	// Like typed input, the tap makes this client the input owner before
	// the helper writes, so the output it causes follows the announcement.
	tab.mu.Lock()
	ownerChanged := tab.noteInputClientLocked(client)
	tab.mu.Unlock()
	if ownerChanged {
		m.syncInputOwner(ctx, tab)
	}
	sent, err := tapper.TapDurablePrompt(ctx, base.PromptTap{
		Generation: request.Generation, Epoch: request.Epoch, Fingerprint: request.Fingerprint, Option: request.Option,
	})
	if err != nil {
		return err
	}
	tab.mu.Lock()
	controlChanged := !tab.closed && tab.noteInputLocked(clientID(client), m.now())
	var control ControlEvent
	if controlChanged {
		control = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if controlChanged {
		m.emit(context.Background(), TopicTerminalControl, control)
	}
	m.transcriptInput(ctx, tab, []byte(sent))
	return nil
}
