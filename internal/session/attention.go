package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// TopicTerminalAttention carries a tab's attention state after each change.
const TopicTerminalAttention = "terminal://attention"

// Attention state sources.
const (
	// AttentionSourceHelper: the durable helper derived the state from the
	// session's own output, independent of any attached client.
	AttentionSourceHelper = "helper"
	// AttentionSourceObserved: the tab does not run in a helper; this
	// process derived the state from the output it received with the same
	// rules. Detected prompts are never tappable.
	AttentionSourceObserved = "observed"
)

// AttentionUnknown is reported while the state cannot be known: the tab is
// not connected, or its helper reports no state. It is never guessed from
// an earlier state.
const AttentionUnknown = "unknown"

// AttentionEvent is a tab's attention state.
type AttentionEvent struct {
	TabID string `json:"tabId"`
	// State is running, needs_me, just_finished, idle, exited or unknown.
	State string `json:"state"`
	// Confidence is signal or heuristic; heuristic states were inferred
	// from the screen shape. Empty for unknown.
	Confidence string `json:"confidence,omitempty"`
	Source     string `json:"source,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Since is when the state began (RFC 3339 with nanoseconds). It
	// identifies the state to terminal_attention_seen.
	Since string `json:"since,omitempty"`
	// Seen reports that a client showed this needs_me or just_finished
	// state to its user.
	Seen     bool `json:"seen,omitempty"`
	Notable  bool `json:"notable,omitempty"`
	ExitCode *int `json:"exitCode,omitempty"`
	// Version increases with every published change of any tab of this
	// process; a client keeps the highest version it saw per tab.
	Version uint64 `json:"version"`
}

func (e AttentionEvent) sameAs(other AttentionEvent) bool {
	sameExit := (e.ExitCode == nil) == (other.ExitCode == nil) && (e.ExitCode == nil || *e.ExitCode == *other.ExitCode)
	return e.State == other.State && e.Confidence == other.Confidence && e.Source == other.Source &&
		e.Reason == other.Reason && e.Since == other.Since && e.Seen == other.Seen && e.Notable == other.Notable && sameExit
}

// attentionRegistry holds the attention state of every tab whose output
// pump runs.
type attentionRegistry struct {
	mu      sync.Mutex
	entries map[string]*tabAttention
	// token identifies each pump's watch; version orders published states.
	token   uint64
	version uint64
}

type tabAttention struct {
	tab   *Tab
	token uint64
	// reporter is the helper attachment of a durable tab; observed the
	// tracker of a tab that does not run in a helper. At most one is set.
	reporter base.DurableStateReporter
	observed *supervisor.AttentionTracker
	// cols and rows are the size observed was last told.
	cols, rows int
	last       AttentionEvent
	published  bool
}

// watchAttention follows the attention state of the tab whose output pump
// starts with ctx: a durable tab reports its helper's state, any other tab
// the state derived from its output. When ctx ends the state becomes exited
// or, when the tab did not exit, unknown until the next pump.
func (m *Manager) watchAttention(ctx context.Context, tab *Tab, generation uint64) {
	tab.mu.Lock()
	durable, lost := tab.durable, tab.durableLost
	cols, rows := int(tab.cols), int(tab.rows)
	current := !tab.closed && tab.generation == generation
	tab.mu.Unlock()
	if !current {
		return
	}
	reporter, _ := durable.(base.DurableStateReporter)
	r := &m.attention
	r.mu.Lock()
	entry := r.entryLocked(tab)
	r.token++
	token := r.token
	previous := entry.observed
	entry.token, entry.reporter, entry.observed = token, reporter, nil
	if durable == nil && !lost {
		entry.observed = supervisor.NewAttentionTracker(cols, rows, func() { m.refreshAttention(tab, token) })
		entry.cols, entry.rows = cols, rows
	}
	r.mu.Unlock()
	if previous != nil {
		// No longer current: its final change is not published.
		previous.Exit(nil)
	}
	m.refreshAttention(tab, token)
	var updates <-chan struct{}
	if reporter != nil {
		updates = reporter.DurableStateUpdates()
	}
	m.startGoroutine(func() {
		for {
			select {
			case <-updates:
				m.refreshAttention(tab, token)
			case <-ctx.Done():
				m.endAttention(tab, token)
				return
			}
		}
	})
}

func (r *attentionRegistry) entryLocked(tab *Tab) *tabAttention {
	if r.entries == nil {
		r.entries = make(map[string]*tabAttention)
	}
	entry := r.entries[tab.ID]
	if entry == nil || entry.tab != tab {
		entry = &tabAttention{tab: tab}
		r.entries[tab.ID] = entry
	}
	return entry
}

// current returns the tab's entry while token is its current watch.
func (r *attentionRegistry) currentLocked(tab *Tab, token uint64) *tabAttention {
	entry := r.entries[tab.ID]
	if entry == nil || entry.tab != tab || entry.token != token {
		return nil
	}
	return entry
}

// refreshAttention publishes the tab's current state when it changed.
func (m *Manager) refreshAttention(tab *Tab, token uint64) {
	r := &m.attention
	r.mu.Lock()
	entry := r.currentLocked(tab, token)
	if entry == nil {
		r.mu.Unlock()
		return
	}
	var event AttentionEvent
	switch {
	case entry.reporter != nil:
		event = attentionFromState(entry.reporter.DurableSessionState(), AttentionSourceHelper)
	case entry.observed != nil:
		event = attentionFromState(entry.observed.State(), AttentionSourceObserved)
	default:
		event = AttentionEvent{State: AttentionUnknown}
	}
	m.publishAttentionLocked(entry, event)
}

// endAttention runs when the pump of a watch ends: the tab exited, or its
// connection is gone and the state is unknown until the next pump.
func (m *Manager) endAttention(tab *Tab, token uint64) {
	tab.mu.Lock()
	exited, closed := tab.exited || tab.durableLost, tab.closed
	tab.mu.Unlock()
	r := &m.attention
	r.mu.Lock()
	entry := r.currentLocked(tab, token)
	if entry == nil {
		r.mu.Unlock()
		return
	}
	observed := entry.observed
	source := entry.last.Source
	entry.reporter, entry.observed = nil, nil
	if closed {
		delete(r.entries, tab.ID)
		r.mu.Unlock()
	} else {
		event := AttentionEvent{State: AttentionUnknown, Reason: "disconnected"}
		if exited {
			event = AttentionEvent{State: base.SessionExited, Confidence: base.ConfidenceSignal, Source: source, Reason: "exit"}
			if entry.last.State == base.SessionExited {
				event = entry.last
			}
		}
		m.publishAttentionLocked(entry, event)
	}
	if observed != nil {
		observed.Exit(nil)
	}
}

// attentionExited publishes the exit of a tab whatever ended it: its pump,
// a reconnect that could not resume it, or a remote exit. The watch of the
// ended runtime stops following it.
func (m *Manager) attentionExited(event ExitEvent) {
	m.mu.Lock()
	tab := m.tabs[event.TabID]
	m.mu.Unlock()
	if tab == nil {
		return
	}
	r := &m.attention
	r.mu.Lock()
	entry := r.entryLocked(tab)
	observed := entry.observed
	r.token++
	entry.token, entry.reporter, entry.observed = r.token, nil, nil
	exited := AttentionEvent{State: base.SessionExited, Confidence: base.ConfidenceSignal, Source: entry.last.Source, Reason: "exit"}
	if event.ExitCode != nil {
		code := *event.ExitCode
		exited.ExitCode = &code
	}
	if entry.last.State == base.SessionExited && exited.ExitCode == nil {
		exited.ExitCode = entry.last.ExitCode
	}
	m.publishAttentionLocked(entry, exited)
	if observed != nil {
		observed.Exit(nil)
	}
}

// publishAttentionLocked records and emits event unless it repeats the last
// published state. It releases the registry lock.
func (m *Manager) publishAttentionLocked(entry *tabAttention, event AttentionEvent) {
	r := &m.attention
	event.TabID = entry.tab.ID
	if entry.published && entry.last.sameAs(event) {
		r.mu.Unlock()
		return
	}
	r.version++
	event.Version = r.version
	entry.last, entry.published = event, true
	r.mu.Unlock()
	m.emitAttention(entry.tab, event)
}

func (m *Manager) emitAttention(tab *Tab, event AttentionEvent) {
	if m.emitter == nil {
		return
	}
	ctx := context.Background()
	if session := tab.session; session != nil {
		session.mu.Lock()
		owner := session.userID
		session.mu.Unlock()
		if owner != "" {
			ctx = ipc.WithEventOwner(ctx, owner)
		}
	}
	_ = m.emitter.EmitSessionEvent(ctx, Event{Topic: TopicTerminalAttention, Payload: event})
}

func attentionFromState(state *base.SessionState, source string) AttentionEvent {
	if state == nil {
		return AttentionEvent{State: AttentionUnknown, Source: source, Reason: "unavailable"}
	}
	event := AttentionEvent{
		State: state.State, Confidence: state.Confidence, Source: source, Reason: state.Reason,
		Seen: state.Seen, Notable: state.Notable,
	}
	if !state.Since.IsZero() {
		event.Since = state.Since.UTC().Format(time.RFC3339Nano)
	}
	if state.ExitCode != nil {
		code := *state.ExitCode
		event.ExitCode = &code
	}
	return event
}

// observeAttentionOutput feeds tab output to the observed tracker of a tab
// that does not run in a helper.
func (m *Manager) observeAttentionOutput(tab *Tab, data []byte) {
	tab.mu.Lock()
	cols, rows := int(tab.cols), int(tab.rows)
	tab.mu.Unlock()
	r := &m.attention
	r.mu.Lock()
	entry := r.entries[tab.ID]
	if entry == nil || entry.tab != tab || entry.observed == nil {
		r.mu.Unlock()
		return
	}
	tracker := entry.observed
	resize := cols > 0 && rows > 0 && (cols != entry.cols || rows != entry.rows)
	if resize {
		entry.cols, entry.rows = cols, rows
	}
	r.mu.Unlock()
	// The tracker reports changes through refreshAttention, which takes the
	// registry lock: it is never called with the lock held.
	if resize {
		tracker.Resize(cols, rows)
	}
	tracker.Output(data)
}

// observeAttentionInput records input written to a tab that does not run in
// a helper; a helper sees the input itself.
func (m *Manager) observeAttentionInput(tab *Tab) {
	r := &m.attention
	r.mu.Lock()
	var tracker *supervisor.AttentionTracker
	if entry := r.entries[tab.ID]; entry != nil && entry.tab == tab {
		tracker = entry.observed
	}
	r.mu.Unlock()
	if tracker != nil {
		tracker.Input()
	}
}

// ListAttentionFor returns the attention state of every tab the requester
// may see. A tab without a running output pump is unknown.
func (m *Manager) ListAttentionFor(ctx context.Context) ([]AttentionEvent, error) {
	infos, err := m.ListTabsFor(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	tabs := make(map[string]*Tab, len(m.tabs))
	for id, tab := range m.tabs {
		tabs[id] = tab
	}
	m.mu.Unlock()
	r := &m.attention
	r.mu.Lock()
	defer r.mu.Unlock()
	// Drop the entries of tabs that are gone.
	for id, entry := range r.entries {
		if tabs[id] != entry.tab {
			delete(r.entries, id)
		}
	}
	result := make([]AttentionEvent, 0, len(infos))
	for _, info := range infos {
		entry := r.entries[info.ID]
		if entry == nil || !entry.published {
			result = append(result, AttentionEvent{TabID: info.ID, State: AttentionUnknown})
			continue
		}
		result = append(result, entry.last)
	}
	return result, nil
}

// MarkAttentionSeenFor records that the requester's user saw the tab's
// needs_me or just_finished state that began at since. The seen state
// reaches every client of the tab, through the helper for a durable tab.
// A state that has moved on since is left alone.
func (m *Manager) MarkAttentionSeenFor(ctx context.Context, tabID, since string) error {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return fmt.Errorf("%w: attention since %q: %v", ErrInvalidOptions, since, err)
	}
	r := &m.attention
	r.mu.Lock()
	var reporter base.DurableStateReporter
	var tracker *supervisor.AttentionTracker
	if entry := r.entries[tab.ID]; entry != nil && entry.tab == tab {
		reporter, tracker = entry.reporter, entry.observed
	}
	r.mu.Unlock()
	switch {
	case reporter != nil:
		return reporter.MarkDurableStateSeen(ctx, at)
	case tracker != nil:
		tracker.MarkSeen(at)
	}
	return nil
}
