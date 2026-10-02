package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

// Options configures a Manager. Dir is dedicated to task records and spool
// files; unrecognized files are left alone. Non-positive retention values
// select the defaults.
type Options struct {
	Dir         string
	HeadBytes   int
	TailBytes   int
	MaxRetained int
	Hooks       Hooks
	Starter     Starter
}

// Manager owns persistent execution tasks rooted at one directory. A single
// Manager writes a directory at a time; Open reconciles whatever a previous
// lifetime left behind.
type Manager struct {
	dir         string
	headLimit   int64
	tailLimit   int64
	maxRetained int
	hooks       Hooks
	starter     Starter

	mu     sync.RWMutex
	tasks  map[string]*task
	closed bool
}

// task is the in-memory state of one execution. info.ID, info.Owner, and
// info.Command are immutable after creation; everything else is guarded by
// mu. Lock order throughout the package: Manager.mu, then task.mu, then
// spool.mu.
type task struct {
	mu       sync.Mutex
	info     Info
	spool    *spool
	proc     Process
	done     chan struct{} // closed after reaping and the terminal transition
	finished bool
	reaped   bool
	detached bool
}

// Open creates or reopens a manager. Records still marked running belong to
// processes whose fate is uncertain, so they transition to StateInterrupted
// without re-executing anything; orphaned spool files from commands that
// never detached are removed.
func Open(opts Options) (*Manager, error) {
	if opts.Dir == "" {
		return nil, errors.New("tasks: options Dir is required")
	}
	m := &Manager{
		dir:         opts.Dir,
		headLimit:   int64(opts.HeadBytes),
		tailLimit:   int64(opts.TailBytes),
		maxRetained: opts.MaxRetained,
		hooks:       opts.Hooks,
		starter:     opts.Starter,
		tasks:       make(map[string]*task),
	}
	if m.headLimit <= 0 {
		m.headLimit = DefaultHeadBytes
	}
	if m.tailLimit <= 0 {
		m.tailLimit = DefaultTailBytes
	}
	if m.maxRetained <= 0 {
		m.maxRetained = DefaultMaxRetained
	}
	if m.starter == nil {
		m.starter = LocalStarter{}
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.enforceRetention()
	return m, nil
}

func (m *Manager) load() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		base := strings.TrimSuffix(name, ".json")
		path := filepath.Join(m.dir, name)
		raw, readErr := os.ReadFile(path)
		var info Info
		if readErr != nil || json.Unmarshal(raw, &info) != nil ||
			info.ID != base || !validTaskID(base) || !info.Owner.valid() || !validState(info.State) {
			// A corrupt record cannot be trusted; drop it and let the
			// orphan sweep below remove its spool.
			os.Remove(path)
			continue
		}
		sp, spoolErr := openSpool(m.dir, base, m.headLimit, m.tailLimit)
		if spoolErr != nil {
			os.Remove(path)
			continue
		}
		done := make(chan struct{})
		close(done)
		t := &task{info: info, spool: sp, done: done, finished: true, reaped: true, detached: true}
		t.info.Detached = true
		t.info.OutputBytes, t.info.DroppedBytes = sp.Stats()
		if info.State == StateRunning {
			now := m.now()
			t.info.State = StateInterrupted
			t.info.EndedAt = &now
			t.info.ExitCode = nil
			t.info.Error = "execution state uncertain after restart"
			if err := writeMetaFile(m.dir, t.info); err != nil {
				return err
			}
		}
		m.tasks[base] = t
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		base, ok := taskFileBase(entry.Name())
		if !ok {
			continue
		}
		if _, loaded := m.tasks[base]; !loaded {
			os.Remove(filepath.Join(m.dir, entry.Name()))
		}
	}
	return nil
}

func validState(s State) bool { return s == StateRunning || s.Terminal() }

func validTaskID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// taskFileBase maps a record, spool, or atomic-write temp file name to its
// task ID.
func taskFileBase(name string) (string, bool) {
	name = strings.TrimSuffix(name, ".tmp")
	for _, suffix := range []string{".json", ".head", ".tail", ".idx"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), true
		}
	}
	return "", false
}

// Run starts a command for owner. syncTimeout selects the synchronous
// window: negative waits for completion without ever detaching, zero detaches
// immediately, and positive detaches once the window elapses. Commands that
// complete within the window return their result directly and leave no task
// record or spool files. A caller context cancellation after startup detaches
// the task (or kills it when detaching was disabled); it never implicitly
// kills a detached task.
func (m *Manager) Run(ctx context.Context, owner Owner, cmd Command, syncTimeout time.Duration) (Result, error) {
	if !owner.valid() {
		return Result{}, ErrInvalidOwner
	}
	if cmd.Path == "" {
		return Result{}, ErrInvalidCommand
	}
	if err := m.authorize(ctx, ActionStart, owner, nil); err != nil {
		return Result{}, err
	}
	if err := m.checkOpen(); err != nil {
		return Result{}, err
	}
	id := m.newID()
	if !validTaskID(id) {
		return Result{}, fmt.Errorf("tasks: generated invalid task id %q", id)
	}
	sp, err := createSpool(m.dir, id, m.headLimit, m.tailLimit)
	if err != nil {
		return Result{}, err
	}
	t := &task{
		info: Info{
			ID:        id,
			Owner:     owner,
			Command:   Command{Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir},
			State:     StateRunning,
			CreatedAt: m.now(),
		},
		spool: sp,
		done:  make(chan struct{}),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		sp.Close()
		removeSpoolFiles(m.dir, id)
		return Result{}, ErrClosed
	}
	m.tasks[id] = t
	m.mu.Unlock()
	proc, err := m.starter.Start(ctx, cmd, sp)
	if err != nil {
		m.mu.Lock()
		delete(m.tasks, id)
		m.mu.Unlock()
		sp.Close()
		removeSpoolFiles(m.dir, id)
		return Result{}, err
	}
	t.mu.Lock()
	t.proc = proc
	stopped := t.finished
	t.mu.Unlock()
	if stopped {
		// Kill or Close won the race before the process handle was
		// published; the terminal state is fixed, so just reap the process.
		_ = proc.Kill()
	}
	go m.supervise(t, proc)

	if syncTimeout < 0 {
		select {
		case <-t.done:
			return m.syncResult(t)
		case <-ctx.Done():
			m.requestStop(t, StateKilled)
			<-t.done
			_, _ = m.syncResult(t)
			return Result{}, ctx.Err()
		}
	}
	if syncTimeout == 0 {
		return m.detachOrComplete(t)
	}
	timer := time.NewTimer(syncTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
		return m.syncResult(t)
	case <-timer.C:
		return m.detachOrComplete(t)
	case <-ctx.Done():
		return m.detachOrComplete(t)
	}
}

// supervise reaps the process and performs the natural terminal transition.
// If Kill or Close already transitioned the task, only the persisted output
// statistics are refreshed; the terminal state never changes again.
func (m *Manager) supervise(t *task, proc Process) {
	code, waitErr := proc.Wait()
	spoolErr := t.spool.Close()
	t.mu.Lock()
	t.reaped = true
	t.proc = nil
	if !t.finished {
		state := StateSucceeded
		errText := ""
		switch {
		case waitErr != nil:
			state = StateFailed
			errText = waitErr.Error()
		case spoolErr != nil:
			state = StateFailed
			errText = spoolErr.Error()
		case code != 0:
			state = StateFailed
		}
		m.finishLocked(t, state, &code, errText)
	} else if t.detached {
		_ = m.writeMetaLocked(t)
	}
	close(t.done)
	t.mu.Unlock()
	m.enforceRetention()
}

// finishLocked is the only terminal transition. The first call wins; later
// calls are no-ops, which gives every task exactly one terminal state.
func (m *Manager) finishLocked(t *task, state State, exitCode *int, errText string) {
	if t.finished {
		return
	}
	t.finished = true
	now := m.now()
	t.info.State = state
	t.info.EndedAt = &now
	t.info.ExitCode = exitCode
	t.info.Error = errText
	if t.detached {
		_ = m.writeMetaLocked(t)
	}
}

// detachOrComplete persists the task when its synchronous window ended, or
// falls through to synchronous completion when the process already exited.
func (m *Manager) detachOrComplete(t *task) (Result, error) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		<-t.done
		return m.syncResult(t)
	}
	t.detached = true
	t.info.Detached = true
	if err := m.writeMetaLocked(t); err != nil {
		t.detached = false
		t.info.Detached = false
		t.mu.Unlock()
		m.requestStop(t, StateKilled)
		<-t.done
		_, _ = m.syncResult(t)
		return Result{}, err
	}
	res := Result{Info: t.snapshotLocked(), Detached: true}
	out, err := t.spool.Snapshot()
	t.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	res.Output = out
	return res, nil
}

// syncResult collects a completed result and, unless the task detached,
// removes every trace of it from the map and the spool directory.
func (m *Manager) syncResult(t *task) (Result, error) {
	t.mu.Lock()
	detached := t.detached
	info := t.snapshotLocked()
	out, err := t.spool.Snapshot()
	t.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	if !detached {
		m.mu.Lock()
		if m.tasks[info.ID] == t {
			delete(m.tasks, info.ID)
		}
		m.mu.Unlock()
		removeTaskFiles(m.dir, info.ID)
	}
	return Result{Info: info, Output: out, Detached: detached}, nil
}

// requestStop transitions a live task to a terminal state and then signals
// the process. Signaling an already-exited process is a harmless no-op.
func (m *Manager) requestStop(t *task, state State) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	proc := t.proc
	m.finishLocked(t, state, nil, stopMessage(state))
	t.mu.Unlock()
	if proc != nil {
		_ = proc.Kill()
	}
}

func stopMessage(state State) string {
	if state == StateInterrupted {
		return "execution interrupted by manager shutdown"
	}
	return ""
}

// Get returns the current snapshot of one owned task.
func (m *Manager) Get(ctx context.Context, owner Owner, id string) (Info, error) {
	t, err := m.lookup(ctx, ActionGet, owner, id)
	if err != nil {
		return Info{}, err
	}
	t.mu.Lock()
	info := t.snapshotLocked()
	t.mu.Unlock()
	return info, nil
}

// List returns the owner's tasks, including live synchronous-window tasks,
// ordered by creation time then ID.
func (m *Manager) List(ctx context.Context, owner Owner) ([]Info, error) {
	if !owner.valid() {
		return nil, ErrInvalidOwner
	}
	if err := m.checkOpen(); err != nil {
		return nil, err
	}
	if err := m.authorize(ctx, ActionList, owner, nil); err != nil {
		return nil, err
	}
	m.mu.RLock()
	var owned []*task
	for _, t := range m.tasks {
		if t.info.Owner == owner {
			owned = append(owned, t)
		}
	}
	m.mu.RUnlock()
	infos := make([]Info, 0, len(owned))
	for _, t := range owned {
		t.mu.Lock()
		infos = append(infos, t.snapshotLocked())
		t.mu.Unlock()
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].CreatedAt.Equal(infos[j].CreatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].CreatedAt.Before(infos[j].CreatedAt)
	})
	return infos, nil
}

// Output returns the retained head-plus-tail snapshot of an owned task.
func (m *Manager) Output(ctx context.Context, owner Owner, id string) (Output, error) {
	t, err := m.lookup(ctx, ActionOutput, owner, id)
	if err != nil {
		return Output{}, err
	}
	return t.spool.Snapshot()
}

// ReadOutput incrementally reads an owned task's stream at a logical offset.
// Offsets inside the elided middle resume at the tail with Gap set, so a
// reader always observes the pre-detach head followed by the latest tail.
func (m *Manager) ReadOutput(ctx context.Context, owner Owner, id string, offset int64, max int) (Chunk, error) {
	t, err := m.lookup(ctx, ActionOutput, owner, id)
	if err != nil {
		return Chunk{}, err
	}
	return t.spool.Read(offset, max)
}

// Kill terminates an owned running task. It is idempotent: killing a task
// that already reached a terminal state returns that state unchanged.
func (m *Manager) Kill(ctx context.Context, owner Owner, id string) (Info, error) {
	t, err := m.lookup(ctx, ActionKill, owner, id)
	if err != nil {
		return Info{}, err
	}
	m.requestStop(t, StateKilled)
	t.mu.Lock()
	info := t.snapshotLocked()
	t.mu.Unlock()
	return info, nil
}

// Close stops every live task as StateInterrupted, waits for reaping, and
// enforces retention. Detached records remain readable after reopening.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	all := make([]*task, 0, len(m.tasks))
	for _, t := range m.tasks {
		all = append(all, t)
	}
	m.mu.Unlock()
	for _, t := range all {
		m.requestStop(t, StateInterrupted)
	}
	var err error
	for _, t := range all {
		select {
		case <-t.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err != nil {
			break
		}
	}
	m.enforceRetention()
	return err
}

// lookup resolves an owned task and authorizes the action. Tasks owned by a
// different object are indistinguishable from missing tasks.
func (m *Manager) lookup(ctx context.Context, action Action, owner Owner, id string) (*task, error) {
	if !owner.valid() {
		return nil, ErrInvalidOwner
	}
	if err := m.checkOpen(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	t := m.tasks[id]
	m.mu.RUnlock()
	if t == nil {
		return nil, ErrNotFound
	}
	t.mu.Lock()
	if t.info.Owner != owner {
		t.mu.Unlock()
		return nil, ErrNotFound
	}
	info := t.snapshotLocked()
	t.mu.Unlock()
	if err := m.authorize(ctx, action, owner, &info); err != nil {
		return nil, err
	}
	return t, nil
}

func (m *Manager) checkOpen() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return ErrClosed
	}
	return nil
}

func (m *Manager) authorize(ctx context.Context, action Action, owner Owner, info *Info) error {
	if m.hooks.Authorize == nil {
		return nil
	}
	return m.hooks.Authorize(ctx, action, owner, info)
}

func (m *Manager) newID() string {
	if m.hooks.NewID != nil {
		return m.hooks.NewID()
	}
	return ids.New()
}

func (m *Manager) now() time.Time {
	if m.hooks.Now != nil {
		return m.hooks.Now()
	}
	return time.Now()
}

// enforceRetention bounds the retained list: the oldest terminal tasks beyond
// MaxRetained lose their record and spool. Running or not-yet-reaped tasks
// are never evicted.
func (m *Manager) enforceRetention() {
	m.mu.Lock()
	var terminal []*task
	for _, t := range m.tasks {
		t.mu.Lock()
		eligible := t.detached && t.finished && t.reaped
		t.mu.Unlock()
		if eligible {
			terminal = append(terminal, t)
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		if terminal[i].info.CreatedAt.Equal(terminal[j].info.CreatedAt) {
			return terminal[i].info.ID < terminal[j].info.ID
		}
		return terminal[i].info.CreatedAt.Before(terminal[j].info.CreatedAt)
	})
	excess := len(terminal) - m.maxRetained
	var victims []string
	if excess > 0 {
		for _, t := range terminal[:excess] {
			if m.tasks[t.info.ID] == t {
				delete(m.tasks, t.info.ID)
				victims = append(victims, t.info.ID)
			}
		}
	}
	m.mu.Unlock()
	for _, id := range victims {
		removeTaskFiles(m.dir, id)
	}
}

func (t *task) snapshotLocked() Info {
	info := t.info
	info.OutputBytes, info.DroppedBytes = t.spool.Stats()
	return info
}

func (m *Manager) writeMetaLocked(t *task) error {
	info := t.snapshotLocked()
	t.info.OutputBytes = info.OutputBytes
	t.info.DroppedBytes = info.DroppedBytes
	return writeMetaFile(m.dir, info)
}

func writeMetaFile(dir string, info Info) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, info.ID+".json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, info.ID+".json"))
}

func removeTaskFiles(dir, id string) {
	removeSpoolFiles(dir, id)
	os.Remove(filepath.Join(dir, id+".json"))
	os.Remove(filepath.Join(dir, id+".json.tmp"))
}
