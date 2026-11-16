package tools

import "context"

// A repeat read_screen of a normal screen returns only what changed since the
// previous read, which is only sound while that previous read is still in the
// reader's context. The main agent remembers its reads per conversation, so a
// follow-up message continues from them, and the agent forgets them whenever
// it compacts or truncates the history. A subagent starts with its own
// context, so it never reuses a read; a call made outside an Execution keeps
// the reads of its job.

type screenMemoryKey struct{}

type screenMemory struct {
	off          bool
	conversation string
}

// maxScreenMemoryConversations bounds the conversations whose reads are kept;
// past it the oldest kept conversation is forgotten and reads there start over
// with the whole screen.
const maxScreenMemoryConversations = 256

func (e *Execution) screenMemoryContext(ctx context.Context) context.Context {
	if e.Subagent {
		return context.WithValue(ctx, screenMemoryKey{}, screenMemory{off: true})
	}
	if e.ConversationID != "" {
		return context.WithValue(ctx, screenMemoryKey{}, screenMemory{conversation: e.ConversationID})
	}
	return ctx
}

// swapScreenRead records a plain read_screen of tabID and returns the read
// before it in the same memory, if any.
func (r *Registry) swapScreenRead(ctx context.Context, jobID, tabID string, read screenRead) (screenRead, bool) {
	memory, _ := ctx.Value(screenMemoryKey{}).(screenMemory)
	switch {
	case memory.off:
		return screenRead{}, false
	case memory.conversation == "":
		return r.state(jobID).swapScreenRead(tabID, read)
	}
	r.screenMu.Lock()
	defer r.screenMu.Unlock()
	if r.screenReads == nil {
		r.screenReads = make(map[string]map[string]screenRead)
	}
	reads := r.screenReads[memory.conversation]
	if reads == nil {
		if len(r.screenReads) >= maxScreenMemoryConversations && len(r.screenOrder) > 0 {
			delete(r.screenReads, r.screenOrder[0])
			r.screenOrder = r.screenOrder[1:]
		}
		reads = make(map[string]screenRead)
		r.screenReads[memory.conversation] = reads
		r.screenOrder = append(r.screenOrder, memory.conversation)
	}
	previous, ok := reads[tabID]
	reads[tabID] = read
	return previous, ok
}

// ForgetScreenReads drops the conversation's remembered reads, so its next
// read of every terminal returns the whole screen. The agent calls it when the
// history the model sees no longer holds those reads (compaction, a summary,
// a truncated or deleted conversation).
func (r *Registry) ForgetScreenReads(conversationID string) {
	if r == nil || conversationID == "" {
		return
	}
	r.screenMu.Lock()
	defer r.screenMu.Unlock()
	if _, ok := r.screenReads[conversationID]; !ok {
		return
	}
	delete(r.screenReads, conversationID)
	for index, id := range r.screenOrder {
		if id == conversationID {
			r.screenOrder = append(r.screenOrder[:index], r.screenOrder[index+1:]...)
			break
		}
	}
}

// RememberedScreenReads reports how many terminals the conversation has a
// remembered read of; it exists for diagnostics and tests.
func (r *Registry) RememberedScreenReads(conversationID string) int {
	r.screenMu.Lock()
	defer r.screenMu.Unlock()
	return len(r.screenReads[conversationID])
}

// RememberScreenReadForTest records a read as if the main agent of the
// conversation had read tabID; tests of other packages use it.
func (r *Registry) RememberScreenReadForTest(conversationID, tabID string, seq uint64) {
	ctx := context.WithValue(context.Background(), screenMemoryKey{}, screenMemory{conversation: conversationID})
	r.swapScreenRead(ctx, "", tabID, screenRead{seq: seq, cols: 80, rows: 24})
}
