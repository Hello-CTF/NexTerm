package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

type coreTerminal struct {
	*terminal.Tab
	output            *terminal.Output
	recordingOpMu     sync.Mutex
	recordingOpActive atomic.Int32
}

func (t *coreTerminal) Feed(raw []byte) {
	_ = t.output.Chunk(context.Background(), raw)
}

// FeedWith overrides the embedded Tab's so options reach the screen through
// the same output path (recording and fanout) as Feed.
func (t *coreTerminal) FeedWith(raw []byte, options terminal.FeedOptions) {
	_ = t.output.ChunkWith(context.Background(), raw, options)
}

// FeedReplay overrides the embedded Tab's for the same reason.
func (t *coreTerminal) FeedReplay(raw []byte) {
	t.FeedWith(raw, terminal.FeedOptions{Replay: true})
}

func (t *coreTerminal) Close() {
	t.StopRecording()
	t.output.Close()
	t.Tab.Close()
}

func (t *coreTerminal) lockRecordingOperation() {
	t.recordingOpMu.Lock()
	t.recordingOpActive.Add(1)
}

func (t *coreTerminal) unlockRecordingOperation() {
	t.recordingOpActive.Add(-1)
	t.recordingOpMu.Unlock()
}

type recordingControlEvent struct {
	ctx        context.Context
	event      ControlEvent
	resourceID string
	closed     bool
	publish    bool
	ready      bool
	coalesce   bool
}

type recordingControlQueue struct {
	mu        sync.Mutex
	id        string
	tab       *Tab
	ownerID   string
	pending   map[uint64]recordingControlEvent
	scheduled atomic.Bool
	barrier   bool
	closed    bool
	orderHeld atomic.Bool

	// Control-event coalescing. held, last, lastEmit and timer are owned by the single drain goroutine (flushDue is guarded by mu):
	// an OSC-title-only change inside controlCoalesceWindow of the previous emission is held and replaced by newer ones, and is
	// emitted by the next non-mergeable event, by the closing event, or by the trailing timer, always before anything newer.
	held     *recordingControlEvent
	last     *ControlEvent
	lastEmit time.Time
	timer    *time.Timer
	flushDue bool
}

// controlCoalesceWindow is the trailing merge window for terminal control events that only change the title or working directory.
var controlCoalesceWindow = 150 * time.Millisecond

// controlMergeable reports whether next differs from prev only in the fields safe to merge (latest wins): version and title.
// Size, grid revision, size owner, subscriber/viewer counts, exit and durability changes always flow through in order.
func controlMergeable(prev, next ControlEvent) bool {
	if next.Exited {
		return false
	}
	prev.Version, prev.Title = next.Version, next.Title
	return prev == next
}

// emitCoalescedControl is the drain-side entry for publishing a dequeued control event; it must only run on the drain goroutine.
func (m *Manager) emitCoalescedControl(queue *recordingControlQueue, item recordingControlEvent) {
	if item.coalesce && !item.closed && queue.last != nil && controlMergeable(*queue.last, item.event) {
		last := item.event
		queue.last = &last
		if queue.held != nil || time.Since(queue.lastEmit) < controlCoalesceWindow {
			held := item
			queue.mu.Lock()
			queue.held = &held
			queue.mu.Unlock()
			m.armControlFlush(queue)
			return
		}
	} else {
		m.flushHeldControl(queue)
	}
	last := item.event
	queue.last = &last
	queue.lastEmit = time.Now()
	m.emitRecordingControl(queue, item.ctx, item.event, item.resourceID, item.closed)
}

func (m *Manager) flushHeldControl(queue *recordingControlQueue) {
	queue.mu.Lock()
	held := queue.held
	queue.held = nil
	queue.flushDue = false
	if queue.timer != nil {
		queue.timer.Stop()
		queue.timer = nil
	}
	queue.mu.Unlock()
	if held == nil {
		return
	}
	queue.lastEmit = time.Now()
	m.emitRecordingControl(queue, held.ctx, held.event, held.resourceID, held.closed)
}

func (m *Manager) armControlFlush(queue *recordingControlQueue) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.timer != nil {
		return
	}
	remaining := max(controlCoalesceWindow-time.Since(queue.lastEmit), time.Millisecond)
	queue.timer = time.AfterFunc(remaining, func() { m.fireControlFlush(queue) })
}

// fireControlFlush hands the held event to the drain goroutine; when a drain is already running it retries shortly so the held
// event cannot be stranded by a drain that finished its last check just before flushDue was set.
func (m *Manager) fireControlFlush(queue *recordingControlQueue) {
	queue.mu.Lock()
	queue.timer = nil
	if queue.held == nil {
		queue.mu.Unlock()
		return
	}
	queue.flushDue = true
	if queue.scheduled.CompareAndSwap(false, true) {
		queue.mu.Unlock()
		m.drainRecordingControlQueue(queue, false)
		return
	}
	queue.timer = time.AfterFunc(2*time.Millisecond, func() { m.fireControlFlush(queue) })
	queue.mu.Unlock()
}

type recordingControlMarker struct{}

// coalescibleControlMarker tags a control event from a high-frequency source (the OSC dynamic title) as eligible for merging.
type coalescibleControlMarker struct{}

func withCoalescibleControl(ctx context.Context) context.Context {
	return context.WithValue(ctx, coalescibleControlMarker{}, true)
}

var recordingControlQueues = struct {
	sync.RWMutex
	current map[string]*recordingControlQueue
	all     map[*recordingControlQueue]struct{}
}{current: make(map[string]*recordingControlQueue), all: make(map[*recordingControlQueue]struct{})}

func newRecordingControlQueue(tab *Tab) *recordingControlQueue {
	queue := &recordingControlQueue{tab: tab, pending: make(map[uint64]recordingControlEvent)}
	if tab != nil {
		queue.id = tab.ID
		if tab.session != nil {
			queue.ownerID = tab.session.userID
		}
	}
	return queue
}

func recordingControlOrderForTab(tab *Tab) *recordingControlQueue {
	if tab == nil {
		return nil
	}
	recordingControlQueues.Lock()
	defer recordingControlQueues.Unlock()
	if current := recordingControlQueues.current[tab.ID]; current != nil && current.tab == tab {
		return current
	}
	queue := newRecordingControlQueue(tab)
	recordingControlQueues.current[tab.ID] = queue
	recordingControlQueues.all[queue] = struct{}{}
	return queue
}

func recordingControlOrderForEvent(event ControlEvent) *recordingControlQueue {
	recordingControlQueues.Lock()
	defer recordingControlQueues.Unlock()
	for queue := range recordingControlQueues.all {
		if queue.id != event.TabID {
			continue
		}
		queue.mu.Lock()
		pending, ok := queue.pending[event.Version]
		queue.mu.Unlock()
		if ok && pending.event == event {
			return queue
		}
	}
	if current := recordingControlQueues.current[event.TabID]; current != nil {
		return current
	}
	queue := &recordingControlQueue{id: event.TabID, pending: make(map[uint64]recordingControlEvent)}
	recordingControlQueues.current[event.TabID] = queue
	recordingControlQueues.all[queue] = struct{}{}
	return queue
}

func recordingControlQueueCurrent(queue *recordingControlQueue) bool {
	recordingControlQueues.RLock()
	defer recordingControlQueues.RUnlock()
	return recordingControlQueues.current[queue.id] == queue
}

func removeRecordingControlOrder(queue *recordingControlQueue) {
	recordingControlQueues.Lock()
	delete(recordingControlQueues.all, queue)
	if recordingControlQueues.current[queue.id] == queue {
		delete(recordingControlQueues.current, queue.id)
	}
	recordingControlQueues.Unlock()
}

func (m *Manager) queueRecordingControl(ctx context.Context, event ControlEvent, force bool) {
	if event.Version == 0 {
		ownerID, resourceID, closed := m.eventOwner(TopicTerminalControl, event)
		if ownerID != "" {
			ctx = ipc.WithEventOwner(ctx, ownerID)
		}
		m.emitRecordingControl(nil, ctx, event, resourceID, closed)
		return
	}
	queue := recordingControlOrderForEvent(event)
	if queue.tab == nil {
		ownerID, resourceID, closed := m.eventOwner(TopicTerminalControl, event)
		if ownerID != "" {
			ctx = ipc.WithEventOwner(ctx, ownerID)
		}
		m.emitRecordingControl(nil, ctx, event, resourceID, closed)
		return
	}
	queue.mu.Lock()
	if force {
		queue.barrier = true
	}
	item, ok := queue.pending[event.Version]
	if !ok {
		item = recordingControlEvent{event: event}
		queue.pending[event.Version] = item
	}
	ownerID := queue.ownerID
	resourceID := queue.id
	closed := item.closed || queue.closed
	if ownerID != "" {
		ctx = ipc.WithEventOwner(ctx, ownerID)
	}
	item.ctx = ctx
	item.resourceID = resourceID
	item.closed = closed
	item.publish = m.emitter != nil
	item.coalesce = ctx.Value(coalescibleControlMarker{}) == true
	item.ready = true
	queue.pending[event.Version] = item
	if closed {
		queue.closed = true
	}
	if queue.ownerID == "" {
		queue.ownerID = ownerID
	}
	queue.orderHeld.Store(false)
	queue.mu.Unlock()
	m.registerControlOwner(queue, ownerID)
	m.scheduleRecordingControl(queue)
}

func (m *Manager) registerControlOwner(queue *recordingControlQueue, ownerID string) {
	m.mu.Lock()
	if recordingControlQueueCurrent(queue) {
		m.tabOwners[queue.id] = ownerID
	}
	if m.controlOwners == nil {
		m.controlOwners = make(map[*recordingControlQueue]string)
	}
	m.controlOwners[queue] = ownerID
	m.mu.Unlock()
}

func (m *Manager) discardControlEvent(event ControlEvent) {
	queue := recordingControlOrderForEvent(event)
	queue.mu.Lock()
	if pending, ok := queue.pending[event.Version]; ok && !pending.ready {
		delete(queue.pending, event.Version)
	}
	queue.mu.Unlock()
}

func recordingControlOperationActive(tab *Tab) bool {
	if tab == nil {
		return false
	}
	if core, ok := tab.terminal.(*coreTerminal); ok && core.recordingOpActive.Load() > 0 {
		return true
	}
	if recorder, ok := tab.terminal.(interface {
		RecordingStatus() terminal.RecordingStatus
	}); ok {
		state := recorder.RecordingStatus().State
		return state == terminal.RecordingRunning || state == terminal.RecordingFailed
	}
	return false
}

func recordingControlBlocked(tab *Tab) bool {
	if tab == nil {
		return false
	}
	if len(tab.feedGate) == 0 && tab.feedGate != nil {
		return true
	}
	return recordingControlOperationActive(tab)
}

func (m *Manager) scheduleRecordingControl(queue *recordingControlQueue) {
	if queue.orderHeld.Load() || queue.scheduled.Load() {
		return
	}
	if !queue.mu.TryLock() {
		if queue.scheduled.CompareAndSwap(false, true) {
			go m.drainRecordingControlQueue(queue, false)
		}
		return
	}
	if queue.orderHeld.Load() || queue.scheduled.Load() || len(queue.pending) == 0 {
		queue.mu.Unlock()
		return
	}
	operationActive := recordingControlOperationActive(queue.tab)
	if operationActive {
		queue.barrier = true
	}
	async := queue.barrier || queue.scheduled.Load() || operationActive
	queue.scheduled.Store(true)
	blocked := recordingControlBlocked(queue.tab)
	if async && blocked {
		queue.mu.Unlock()
		go m.drainRecordingControlQueue(queue, false)
		return
	}
	m.drainRecordingControlQueue(queue, true)
}

func (m *Manager) drainRecordingControlQueue(queue *recordingControlQueue, locked bool) {
	for {
		if !locked {
			queue.mu.Lock()
			locked = true
		}
		if queue.flushDue && queue.held != nil {
			queue.mu.Unlock()
			m.flushHeldControl(queue)
			locked = false
			continue
		}
		if queue.orderHeld.Load() {
			queue.scheduled.Store(false)
			queue.mu.Unlock()
			return
		}
		if len(queue.pending) == 0 {
			queue.scheduled.Store(false)
			queue.barrier = false
			remove := queue.closed
			queue.mu.Unlock()
			if remove {
				removeRecordingControlOrder(queue)
			}
			return
		}
		var version uint64
		for candidate := range queue.pending {
			if version == 0 || candidate < version {
				version = candidate
			}
		}
		event := queue.pending[version]
		if !event.ready {
			queue.scheduled.Store(false)
			queue.mu.Unlock()
			return
		}
		delete(queue.pending, version)
		barrier := queue.barrier
		queue.mu.Unlock()
		locked = false

		if barrier {
			m.waitRecordingControl(queue.tab)
		}
		if event.publish {
			m.emitCoalescedControl(queue, event)
		}
		if !queue.mu.TryLock() {
			go m.drainRecordingControlQueue(queue, false)
			return
		}
		locked = true
	}
}

func (m *Manager) waitRecordingControl(tab *Tab) {
	if tab == nil {
		return
	}
	unlockOperation := lockRecordingOperation(tab)
	if tab.feedGate != nil {
		if err := tab.lockFeed(context.Background()); err == nil {
			tab.unlockFeed()
		}
	}
	unlockOperation()
}

func (m *Manager) emitRecordingControl(queue *recordingControlQueue, ctx context.Context, event ControlEvent, resourceID string, closed bool) {
	if m.emitter != nil {
		_ = m.emitter.EmitSessionEvent(ctx, Event{Topic: TopicTerminalControl, Payload: event})
	}
	if !closed {
		return
	}
	m.mu.Lock()
	if queue == nil {
		delete(m.tabOwners, resourceID)
	} else {
		delete(m.controlOwners, queue)
		if recordingControlQueueCurrent(queue) {
			delete(m.tabOwners, queue.id)
		}
	}
	m.mu.Unlock()
}

func lockRecordingOperation(tab *Tab) func() {
	if locker, ok := tab.terminal.(interface {
		lockRecordingOperation()
		unlockRecordingOperation()
	}); ok {
		locker.lockRecordingOperation()
		return locker.unlockRecordingOperation
	}
	return func() {}
}

type terminalFactory struct{}

func (m *Manager) newOutputFanout() *terminal.Fanout {
	return terminal.NewFanoutWithOptions(terminal.FanoutOptions{Logger: m.logger})
}

func (terminalFactory) NewTerminal(config TerminalConfig) (TerminalState, error) {
	encoding := terminal.UTF8
	var err error
	if config.Encoding != "" {
		encoding, err = terminal.ParseEncoding(config.Encoding)
		if err != nil {
			return nil, err
		}
	}
	tab := terminal.NewTab(config.TabID, config.SessionID, int(config.Cols), int(config.Rows), encoding)
	return &coreTerminal{Tab: tab, output: terminal.NewOutput(tab)}, nil
}

func (m *Manager) TerminalSnapshot(tabID string) (terminal.Snapshot, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	core, ok := tab.terminal.(interface{ Snapshot() terminal.Snapshot })
	if !ok {
		return terminal.Snapshot{}, ErrUnsupported
	}
	return core.Snapshot(), nil
}

func (m *Manager) ScreenText(tabID string) (string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return "", err
	}
	core, ok := tab.terminal.(interface{ ScreenText() string })
	if !ok {
		return "", ErrUnsupported
	}
	return core.ScreenText(), nil
}

func (m *Manager) TailLines(tabID string, count int) ([]string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, err
	}
	core, ok := tab.terminal.(interface{ TailLines(int) []string })
	if !ok {
		return nil, ErrUnsupported
	}
	return core.TailLines(count), nil
}

func (m *Manager) ScreenSeq(tabID string) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	return tab.ScreenSeq(), nil
}

// OutputSince returns the raw output bytes produced after seq, capped at
// maxBytes, together with the actual start sequence and the latest sequence.
// When seq has scrolled out of the ring, start reports the oldest retained
// byte and clamped is true so callers can flag the anchor as expired.
func (m *Manager) OutputSince(tabID string, seq uint64, maxBytes int) (data []byte, start, latest uint64, clamped bool, err error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, 0, 0, false, err
	}
	core, ok := tab.terminal.(interface {
		ReplayFrom(uint64, int) ([]byte, uint64, bool)
		LatestSeq() uint64
	})
	if !ok {
		return nil, 0, 0, false, ErrUnsupported
	}
	data, start, clamped = core.ReplayFrom(seq, maxBytes)
	return data, start, core.LatestSeq(), clamped, nil
}

func (m *Manager) TerminalModeState(tabID string) (terminal.State, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return terminal.State{}, err
	}
	core, ok := tab.terminal.(interface{ State() terminal.State })
	if !ok {
		return terminal.State{}, ErrUnsupported
	}
	return core.State(), nil
}

func (m *Manager) RawDump(tabID string, maxBytes int) ([]byte, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, err
	}
	return tab.terminal.Dump(maxBytes), nil
}

func (m *Manager) ExportLog(tabID, path string, maxBytes int) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	core, ok := tab.terminal.(interface {
		ExportLog(string, int) (uint64, error)
	})
	if !ok {
		return 0, ErrUnsupported
	}
	return core.ExportLog(path, maxBytes)
}

func (m *Manager) SwitchEncoding(tabID, encoding string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	parsed, err := terminal.ParseEncoding(encoding)
	if err != nil {
		return err
	}
	core, ok := tab.terminal.(interface{ SwitchEncoding(terminal.Encoding) })
	if !ok {
		return ErrUnsupported
	}
	core.SwitchEncoding(parsed)
	tab.mu.Lock()
	tab.encoding = canonicalEncoding(encoding)
	tab.mu.Unlock()
	return nil
}

func (m *Manager) SetVisible(tabID string, visible bool) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	core, ok := tab.terminal.(interface{ SetVisible(bool) })
	if !ok {
		return ErrUnsupported
	}
	tab.visibilityMu.Lock()
	defer tab.visibilityMu.Unlock()
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	mode := terminalgrid.ModeHidden
	if visible && tab.sizeOwner != "" {
		mode = terminalgrid.ModeController
	} else if visible {
		mode = terminalgrid.ModeObserver
	}
	if tab.grid != nil {
		if err := tab.grid.SetMode(mode); err != nil {
			tab.mu.Unlock()
			return mapGridError(err)
		}
	}
	tab.hidden = !visible
	tab.mu.Unlock()
	core.SetVisible(visible)
	return nil
}

func (m *Manager) StartRecording(tabID, path string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	unlockOperation := lockRecordingOperation(tab)
	defer unlockOperation()
	if err := tab.lockFeed(context.Background()); err != nil {
		return err
	}
	defer tab.unlockFeed()
	core, ok := tab.terminal.(interface{ StartRecording(string) error })
	if !ok {
		return ErrUnsupported
	}
	if recorder, ok := tab.terminal.(interface {
		SetRecordingHandler(func(terminal.RecordingStatus))
	}); ok {
		recorder.SetRecordingHandler(func(_ terminal.RecordingStatus) {
			tab.mu.Lock()
			event := tab.controlEventLocked()
			tab.mu.Unlock()
			ctx := context.WithValue(context.Background(), recordingControlMarker{}, true)
			m.emit(ctx, TopicTerminalControl, event)
		})
	}
	return core.StartRecording(path)
}

func (m *Manager) StopRecording(tabID string) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	unlockOperation := lockRecordingOperation(tab)
	defer unlockOperation()
	if err := tab.lockFeed(context.Background()); err != nil {
		return 0, err
	}
	defer tab.unlockFeed()
	core, ok := tab.terminal.(interface{ StopRecording() (uint64, error) })
	if !ok {
		return 0, ErrUnsupported
	}
	return core.StopRecording()
}

func (m *Manager) RecordingBytes(tabID string) (uint64, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return 0, err
	}
	core, ok := tab.terminal.(interface{ RecordingBytes() uint64 })
	if !ok {
		return 0, ErrUnsupported
	}
	return core.RecordingBytes(), nil
}

func (m *Manager) RecordingStatus(tabID string) (terminal.RecordingStatus, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return terminal.RecordingStatus{}, err
	}
	core, ok := tab.terminal.(interface {
		RecordingStatus() terminal.RecordingStatus
	})
	if !ok {
		return terminal.RecordingStatus{}, ErrUnsupported
	}
	return core.RecordingStatus(), nil
}

func (m *Manager) InjectInternal(ctx context.Context, tabID string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	generation := tab.generation
	tab.mu.Unlock()
	return m.feed(ctx, tab, generation, data)
}

func (m *Manager) WriteInternal(ctx context.Context, tabID string, data []byte) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	release, err := tab.acquireWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	return m.writeInternalLocked(ctx, tab, data)
}

var (
	ErrPasteModeChanged  = errors.New("terminal paste mode changed during write; input may be partially written; do not retry blindly")
	ErrPasteVerification = errors.New("terminal paste mode verification failed after write; input may be partially written; do not retry blindly")
	ErrTabEpisodeChanged = errors.New("terminal tab runtime episode changed before write")
)

func (m *Manager) WritePasteFor(ctx context.Context, tabID, text string) error {
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
	if err := m.writeInternalLocked(ctx, tab, encoded); err != nil {
		return err
	}
	if err := tab.lockFeed(ctx); err != nil {
		return errors.Join(ErrPasteVerification, err)
	}
	defer tab.unlockFeed()
	_, currentGeneration := core.BracketedPasteMode()
	if currentGeneration != generation {
		return ErrPasteModeChanged
	}
	return nil
}

// ErrPasteGuarded refuses a literal paste whose text would be reinterpreted
// as line submissions: without bracketed-paste mode the encoder turns every
// LF into CR, so multi-line text would execute line by line instead of
// pasting literally.
var ErrPasteGuarded = errors.New("literal paste refused: terminal left bracketed-paste mode and the text contains line breaks")

// WritePasteLiteralFor writes text as literal terminal input for callers
// that must never have line breaks reinterpreted as submissions (AI tools
// pasting scripts). The mode snapshot is read inside the real write lock and
// the encoding uses that same copy, so a mode flip between a caller's
// precheck and the write cannot silently change the encoding: when the
// snapshot says bracketed paste is off and the text contains line breaks,
// the write is refused with ErrPasteGuarded before any byte is written.
// force bypasses that refusal, but still writes under the same write lock
// with the same mode snapshot, in arrival order with interactive input —
// force never overrides newer user input. Single-line text is written
// unchanged regardless of mode, and interactive WritePasteFor behavior is
// untouched.
func (m *Manager) WritePasteLiteralFor(ctx context.Context, tabID, text string, force bool) error {
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
	if !bracketed && !force && strings.ContainsAny(text, "\r\n") {
		return ErrPasteGuarded
	}
	if err := m.writeInternalLocked(ctx, tab, encoded); err != nil {
		return err
	}
	if err := tab.lockFeed(ctx); err != nil {
		return errors.Join(ErrPasteVerification, err)
	}
	defer tab.unlockFeed()
	_, currentGeneration := core.BracketedPasteMode()
	if currentGeneration != generation {
		return ErrPasteModeChanged
	}
	return nil
}

func (m *Manager) writeInternalLocked(ctx context.Context, tab *Tab, data []byte) error {
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if !ptyBacked(tab.session.asset.Kind) {
		tab.mu.Unlock()
		return ErrUnsupported
	}
	channel := tab.channel
	generation := tab.generation
	exited := tab.exited
	tab.mu.Unlock()
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if exited || channel == nil {
		return ErrTabClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected, ok := ctx.Value(expectedTabEpisodeContextKey{}).(expectedTabEpisode); ok {
		tab.mu.Lock()
		episode := tab.durableIdentity.Incarnation
		if episode == "" {
			episode = tab.runtimeEpisode
		}
		valid := !tab.closed && !tab.exited && tab.generation == generation && expected.tabID == tab.ID && expected.episode != "" && episode == expected.episode
		tab.mu.Unlock()
		if !valid {
			return ErrTabEpisodeChanged
		}
	}
	if err := writeChannelContext(ctx, channel, data); err != nil {
		return err
	}
	m.transcriptInput(ctx, tab, data)
	return nil
}

func (m *Manager) TerminalSnapshotFor(ctx context.Context, tabID string) (terminal.Snapshot, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return terminal.Snapshot{}, err
	}
	return m.TerminalSnapshot(tabID)
}

func (m *Manager) TerminalWaitSnapshotFor(ctx context.Context, tabID string) (terminal.Snapshot, []string, error) {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return terminal.Snapshot{}, nil, err
	}
	if err := tab.lockFeed(ctx); err != nil {
		return terminal.Snapshot{}, nil, err
	}
	defer tab.unlockFeed()
	snapshot, err := m.TerminalSnapshot(tabID)
	if err != nil {
		return terminal.Snapshot{}, nil, err
	}
	tail, err := m.TailLines(tabID, 30)
	if err != nil {
		return terminal.Snapshot{}, nil, err
	}
	return snapshot, tail, nil
}

func (m *Manager) ScreenTextFor(ctx context.Context, tabID string) (string, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return "", err
	}
	return m.ScreenText(tabID)
}

func (m *Manager) TailLinesFor(ctx context.Context, tabID string, count int) ([]string, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return nil, err
	}
	return m.TailLines(tabID, count)
}

func (m *Manager) RawDumpFor(ctx context.Context, tabID string, maxBytes int) ([]byte, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return nil, err
	}
	return m.RawDump(tabID, maxBytes)
}

func (m *Manager) StartRecordingFor(ctx context.Context, tabID, path string) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.StartRecording(tabID, path)
}

func (m *Manager) StopRecordingFor(ctx context.Context, tabID string) (uint64, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return 0, err
	}
	return m.StopRecording(tabID)
}

func (m *Manager) ScreenSeqFor(ctx context.Context, tabID string) (uint64, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return 0, err
	}
	return m.ScreenSeq(tabID)
}

func (m *Manager) OutputSinceFor(ctx context.Context, tabID string, seq uint64, maxBytes int) (data []byte, start, latest uint64, clamped bool, err error) {
	if _, err = m.TabFor(ctx, tabID); err != nil {
		return nil, 0, 0, false, err
	}
	return m.OutputSince(tabID, seq, maxBytes)
}

func (m *Manager) TerminalModeStateFor(ctx context.Context, tabID string) (terminal.State, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return terminal.State{}, err
	}
	return m.TerminalModeState(tabID)
}

func (m *Manager) ExportLogFor(ctx context.Context, tabID, path string, maxBytes int) (uint64, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return 0, err
	}
	return m.ExportLog(tabID, path, maxBytes)
}

func (m *Manager) SwitchEncodingFor(ctx context.Context, tabID, encoding string) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.SwitchEncoding(tabID, encoding)
}

func (m *Manager) SetVisibleFor(ctx context.Context, tabID string, visible bool) error {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return err
	}
	return m.SetVisible(tabID, visible)
}

func (m *Manager) RecordingBytesFor(ctx context.Context, tabID string) (uint64, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return 0, err
	}
	return m.RecordingBytes(tabID)
}

func (m *Manager) RecordingStatusFor(ctx context.Context, tabID string) (terminal.RecordingStatus, error) {
	if _, err := m.TabFor(ctx, tabID); err != nil {
		return terminal.RecordingStatus{}, err
	}
	return m.RecordingStatus(tabID)
}
