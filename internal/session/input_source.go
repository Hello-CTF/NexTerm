package session

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// TopicTerminalInputSource reports that a non-user writer typed into a tab,
// so the terminal pane can show where the input came from. It is purely
// informational and carries no input bytes.
const TopicTerminalInputSource = "terminal://input-source"

// InputSourceAI marks terminal input written by the AI (send_keys, takeover).
const InputSourceAI = "ai"

type InputSourceEvent struct {
	TabID  string `json:"tabId"`
	Source string `json:"source"`
}

type inputSourceContextKey struct{}

// WithInputSource marks the internal writes made with ctx as coming from
// source; after each successful write the manager emits an InputSourceEvent.
func WithInputSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, inputSourceContextKey{}, source)
}

func inputSourceFrom(ctx context.Context) string {
	source, _ := ctx.Value(inputSourceContextKey{}).(string)
	return source
}

func (m *Manager) emitInputSource(ctx context.Context, tab *Tab) {
	source := inputSourceFrom(ctx)
	if source == "" || m.emitter == nil {
		return
	}
	eventCtx := context.Background()
	if session := tab.session; session != nil {
		session.mu.Lock()
		owner := session.userID
		session.mu.Unlock()
		if owner != "" {
			eventCtx = ipc.WithEventOwner(eventCtx, owner)
		}
	}
	_ = m.emitter.EmitSessionEvent(eventCtx, Event{Topic: TopicTerminalInputSource, Payload: InputSourceEvent{TabID: tab.ID, Source: source}})
}
