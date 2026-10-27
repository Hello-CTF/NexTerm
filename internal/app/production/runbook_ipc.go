package production

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/runbook"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	defaultRunbookMaxActiveRunsPerOwner   = 8
	defaultRunbookMaxRetainedRunsPerOwner = 32
	defaultRunbookRetainedRunTTL          = 24 * time.Hour
)

var errRunbookActiveRunLimit = errors.New("runbook active run limit reached")

type runbookHistoryReader interface {
	ExecBatchRunHistoryGet(context.Context, string) (store.ExecBatchRunHistory, error)
}

// runbookTemplateStore is the persistence contract for per-owner runbook templates.
type runbookTemplateStore interface {
	RunbookTemplateList(context.Context) ([]store.RunbookTemplate, error)
	RunbookTemplateGet(context.Context, string) (store.RunbookTemplate, error)
	RunbookTemplateCreate(context.Context, store.RunbookTemplateInput) (store.RunbookTemplate, error)
	RunbookTemplateUpdate(context.Context, string, store.RunbookTemplateInput) (store.RunbookTemplate, error)
	RunbookTemplateDelete(context.Context, string) error
}

type runbookTemplateRequest struct {
	ID string `json:"id"`
}

type runbookTemplateUpdateRequest struct {
	ID string `json:"id"`
	store.RunbookTemplateInput
}

type runbookRunKey struct {
	ownerID string
	runID   string
}

type runbookRunEntry struct {
	runs       *runbook.Service
	startedAt  time.Time
	lastAccess time.Time
	terminal   bool
}

// runbookIPCService indexes live runs by (ownerID, runID). Each run owns a dedicated
// runbook.Service so terminal runs can be evicted without touching durable history.
type runbookIPCService struct {
	executor runbook.Executor
	history  runbookHistoryReader
	// templates is nil when the history reader does not persist templates.
	templates runbookTemplateStore
	now       func() time.Time

	maxActiveRunsPerOwner   int
	maxRetainedRunsPerOwner int
	retainedRunTTL          time.Duration

	mu   sync.Mutex
	runs map[runbookRunKey]*runbookRunEntry
}

func newRunbookIPCService(executor runbook.Executor, history ...runbookHistoryReader) *runbookIPCService {
	var reader runbookHistoryReader
	if len(history) > 0 {
		reader = history[0]
	}
	templates, _ := reader.(runbookTemplateStore)
	return &runbookIPCService{
		executor: executor, history: reader, templates: templates, now: time.Now,
		maxActiveRunsPerOwner:   defaultRunbookMaxActiveRunsPerOwner,
		maxRetainedRunsPerOwner: defaultRunbookMaxRetainedRunsPerOwner,
		retainedRunTTL:          defaultRunbookRetainedRunTTL,
		runs:                    make(map[runbookRunKey]*runbookRunEntry),
	}
}

func newProductionRunbookIPC(batches *execbatch.Service, history *store.Store) *runbookIPCService {
	var executor runbook.Executor
	if batches != nil {
		executor = batches
	}
	var reader runbookHistoryReader
	if history != nil {
		reader = history
	}
	return newRunbookIPCService(executor, reader)
}

func (s *runbookIPCService) register(dispatcher *ipc.Dispatcher) error {
	if err := ipc.Register(dispatcher, "runbook_run_start", func(ctx context.Context, _ *ipc.Call, input runbook.StartRequest) (runbook.RunState, error) {
		state, err := s.Start(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_get", func(ctx context.Context, _ *ipc.Call, input runbook.RunRequest) (runbook.RunState, error) {
		state, err := s.Get(ctx, ipc.OwnerID(ctx), input.RunID)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_prepare", func(ctx context.Context, _ *ipc.Call, input runbook.PrepareRequest) (runbook.RunState, error) {
		state, err := s.Prepare(ctx, ipc.OwnerID(ctx), input)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_advance", func(ctx context.Context, _ *ipc.Call, input runbook.AdvanceRequest) (runbook.RunState, error) {
		state, err := s.Advance(productionSessionRequestContext(ctx), ipc.OwnerID(ctx), input)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_run_cancel", func(ctx context.Context, _ *ipc.Call, input runbook.RunRequest) (runbook.RunState, error) {
		state, err := s.Cancel(ctx, ipc.OwnerID(ctx), input.RunID)
		if err != nil {
			return runbook.RunState{}, runbookIPCError(err)
		}
		return state, nil
	}); err != nil {
		return err
	}
	if err := s.registerTemplates(dispatcher); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "runbook_run_history_get", func(ctx context.Context, _ *ipc.Call, input runbook.RunRequest) (store.ExecBatchRunHistory, error) {
		if s.history == nil {
			return store.ExecBatchRunHistory{}, ipc.NewError(ipc.CodeUnsupported, "Runbook 运行历史不可用")
		}
		return s.history.ExecBatchRunHistoryGet(ctx, input.RunID)
	})
}

func (s *runbookIPCService) registerTemplates(dispatcher *ipc.Dispatcher) error {
	unavailable := func() error { return ipc.NewError(ipc.CodeUnsupported, "Runbook 模板不可用") }
	if err := ipc.Register(dispatcher, "runbook_template_list", func(ctx context.Context, _ *ipc.Call, _ struct{}) ([]store.RunbookTemplate, error) {
		if s.templates == nil {
			return nil, unavailable()
		}
		return s.templates.RunbookTemplateList(ctx)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_template_get", func(ctx context.Context, _ *ipc.Call, input runbookTemplateRequest) (store.RunbookTemplate, error) {
		if s.templates == nil {
			return store.RunbookTemplate{}, unavailable()
		}
		return s.templates.RunbookTemplateGet(ctx, input.ID)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_template_create", func(ctx context.Context, _ *ipc.Call, input store.RunbookTemplateInput) (store.RunbookTemplate, error) {
		if s.templates == nil {
			return store.RunbookTemplate{}, unavailable()
		}
		return s.templates.RunbookTemplateCreate(ctx, input)
	}); err != nil {
		return err
	}
	if err := ipc.Register(dispatcher, "runbook_template_update", func(ctx context.Context, _ *ipc.Call, input runbookTemplateUpdateRequest) (store.RunbookTemplate, error) {
		if s.templates == nil {
			return store.RunbookTemplate{}, unavailable()
		}
		return s.templates.RunbookTemplateUpdate(ctx, input.ID, input.RunbookTemplateInput)
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "runbook_template_delete", func(ctx context.Context, _ *ipc.Call, input runbookTemplateRequest) (any, error) {
		if s.templates == nil {
			return nil, unavailable()
		}
		return nil, s.templates.RunbookTemplateDelete(ctx, input.ID)
	})
}

// resolveTemplate fills a templateId start request from the caller's own
// template. The steps are the commands snapshotted at save time, so later
// snippet edits never change what a run executes.
func (s *runbookIPCService) resolveTemplate(ctx context.Context, input runbook.StartRequest) (runbook.StartRequest, error) {
	if input.TemplateID == "" {
		return input, nil
	}
	if input.RunbookID != "" || input.RunbookName != "" || len(input.Steps) != 0 {
		return input, fmt.Errorf("%w: templateId cannot be combined with runbookId, runbookName or steps", runbook.ErrInvalid)
	}
	if s.templates == nil {
		return input, ipc.NewError(ipc.CodeUnsupported, "Runbook 模板不可用")
	}
	tpl, err := s.templates.RunbookTemplateGet(ctx, input.TemplateID)
	if err != nil {
		return input, err
	}
	input.RunbookID, input.RunbookName, input.RunbookVersion = tpl.ID, tpl.Name, tpl.Version
	input.Steps = make([]runbook.Step, len(tpl.Steps))
	for i, step := range tpl.Steps {
		input.Steps[i] = runbook.Step{ID: step.ID, SnippetID: step.SnippetID, Name: step.Name, Description: step.Description, Command: step.Command}
	}
	return input, nil
}

func (s *runbookIPCService) Start(ctx context.Context, ownerID string, input runbook.StartRequest) (runbook.RunState, error) {
	input.RunbookVersion = 0
	input, err := s.resolveTemplate(ctx, input)
	if err != nil {
		return runbook.RunState{}, err
	}
	key := runbookRunKey{ownerID: ownerID, runID: input.RunID}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.evictTerminalRunsLocked(ownerID, now)
	if _, exists := s.runs[key]; exists {
		return runbook.RunState{}, fmt.Errorf("%w: %s", runbook.ErrConflict, input.RunID)
	}
	if s.activeRunCountLocked(ownerID) >= s.maxActiveRunsPerOwner {
		return runbook.RunState{}, fmt.Errorf("%w: %s", errRunbookActiveRunLimit, input.RunID)
	}
	entry := &runbookRunEntry{runs: runbook.New(s.executor), startedAt: now, lastAccess: now}
	state, err := entry.runs.Start(ctx, ownerID, input)
	if err != nil {
		return runbook.RunState{}, err
	}
	entry.terminal = runbookRunTerminal(state.Status)
	s.runs[key] = entry
	return state, nil
}

func (s *runbookIPCService) Get(ctx context.Context, ownerID, runID string) (runbook.RunState, error) {
	return s.withRun(ctx, ownerID, runID, func(ctx context.Context, runs *runbook.Service) (runbook.RunState, error) {
		return runs.Get(ctx, ownerID, runID)
	})
}

func (s *runbookIPCService) Prepare(ctx context.Context, ownerID string, input runbook.PrepareRequest) (runbook.RunState, error) {
	return s.withRun(ctx, ownerID, input.RunID, func(ctx context.Context, runs *runbook.Service) (runbook.RunState, error) {
		return runs.Prepare(ctx, ownerID, input)
	})
}

func (s *runbookIPCService) Advance(ctx context.Context, ownerID string, input runbook.AdvanceRequest) (runbook.RunState, error) {
	return s.withRun(ctx, ownerID, input.RunID, func(ctx context.Context, runs *runbook.Service) (runbook.RunState, error) {
		return runs.Advance(ctx, ownerID, input)
	})
}

func (s *runbookIPCService) Cancel(ctx context.Context, ownerID, runID string) (runbook.RunState, error) {
	return s.withRun(ctx, ownerID, runID, func(ctx context.Context, runs *runbook.Service) (runbook.RunState, error) {
		return runs.Cancel(ctx, ownerID, runID)
	})
}

func (s *runbookIPCService) withRun(ctx context.Context, ownerID, runID string, op func(context.Context, *runbook.Service) (runbook.RunState, error)) (runbook.RunState, error) {
	if strings.TrimSpace(runID) == "" {
		return runbook.RunState{}, fmt.Errorf("%w: runId is required", runbook.ErrInvalid)
	}
	key := runbookRunKey{ownerID: ownerID, runID: runID}
	s.mu.Lock()
	entry := s.runs[key]
	if entry == nil {
		s.mu.Unlock()
		return runbook.RunState{}, fmt.Errorf("%w: %s", runbook.ErrNotFound, runID)
	}
	entry.lastAccess = s.now()
	s.mu.Unlock()

	state, err := op(ctx, entry.runs)

	s.mu.Lock()
	if s.runs[key] == entry {
		entry.lastAccess = s.now()
		// Terminal is monotonic for the entry's lifetime: the engine never leaves a
		// terminal state, so only a successful terminal result may set it. Errors
		// return a zero state and a stale in-flight response may be non-terminal;
		// neither may revert a completed run to active.
		if err == nil && runbookRunTerminal(state.Status) {
			entry.terminal = true
		}
	}
	s.mu.Unlock()
	return state, err
}

// evictTerminalRunsLocked drops expired and least-recently-used terminal runs of one owner;
// active runs are never evicted here.
func (s *runbookIPCService) evictTerminalRunsLocked(ownerID string, now time.Time) {
	retained := make([]runbookRunKey, 0, len(s.runs))
	for key, entry := range s.runs {
		if key.ownerID != ownerID || !entry.terminal {
			continue
		}
		if s.retainedRunTTL > 0 && !entry.lastAccess.Add(s.retainedRunTTL).After(now) {
			delete(s.runs, key)
			continue
		}
		retained = append(retained, key)
	}
	if len(retained) <= s.maxRetainedRunsPerOwner {
		return
	}
	sort.Slice(retained, func(i, j int) bool {
		left, right := s.runs[retained[i]], s.runs[retained[j]]
		if left.lastAccess.Equal(right.lastAccess) {
			return retained[i].runID < retained[j].runID
		}
		return left.lastAccess.Before(right.lastAccess)
	})
	for _, key := range retained[:len(retained)-s.maxRetainedRunsPerOwner] {
		delete(s.runs, key)
	}
}

func (s *runbookIPCService) activeRunCountLocked(ownerID string) int {
	count := 0
	for key, entry := range s.runs {
		if key.ownerID == ownerID && !entry.terminal {
			count++
		}
	}
	return count
}

// runbookCleanupReservation pins the entries reserved by one owner-cleanup
// prepare until its once-only commit completes; the commit then releases the
// entry map so a retained replay marker cannot pin the run services and their
// batch outputs for the retention lifetime. Rollback deliberately keeps the
// reservation: a later commit still cancels the reserved snapshot.
type runbookCleanupReservation struct {
	service *runbookIPCService
	ctx     context.Context
	ownerID string

	once     sync.Once
	entries  map[runbookRunKey]*runbookRunEntry
	released bool
}

func (r *runbookCleanupReservation) commit(...string) {
	r.once.Do(func() {
		s := r.service
		s.mu.Lock()
		entries := make(map[runbookRunKey]*runbookRunEntry, len(r.entries))
		for key, reserved := range r.entries {
			if s.runs[key] == reserved {
				entries[key] = reserved
				delete(s.runs, key)
			}
		}
		s.mu.Unlock()
		// Release the reservation payload before the slow cancels: the replay
		// marker must not pin the run services and their batch outputs.
		for key := range r.entries {
			delete(r.entries, key)
		}
		r.entries = nil
		r.released = true
		for key := range entries {
			entries[key].runs.Cancel(context.WithoutCancel(r.ctx), r.ownerID, key.runID)
		}
	})
}

// PrepareOwnerCleanup snapshots the owner's live runs without canceling them; commit and rollback are mutually exclusive.
// A non-nil cutoff bounds the snapshot to runs started at or before the cleanup generation's terminal
// instant (same-millisecond inclusive), so a late cleanup of an evicted event cannot cancel new-generation runs.
// The reservation pins the entry pointers, and the commit only removes and cancels an entry while it is
// still the reserved one: a same-ID replacement started after the reservation is never touched.
func (s *runbookIPCService) PrepareOwnerCleanup(ctx context.Context, ownerID, eventID string, cutoff *time.Time) (func(...string), func()) {
	if ownerID == "" || eventID == "" {
		return func(...string) {}, func() {}
	}
	reservation := s.prepareOwnerCleanupReservation(ctx, ownerID, eventID, cutoff)
	return reservation.commit, func() {}
}

func (s *runbookIPCService) prepareOwnerCleanupReservation(ctx context.Context, ownerID, eventID string, cutoff *time.Time) *runbookCleanupReservation {
	s.mu.Lock()
	snapshot := make(map[runbookRunKey]*runbookRunEntry, len(s.runs))
	for key, entry := range s.runs {
		if key.ownerID != ownerID {
			continue
		}
		if cutoff != nil && !accountCleanupAtOrBeforeGeneration(entry.startedAt, *cutoff) {
			continue
		}
		snapshot[key] = entry
	}
	s.mu.Unlock()
	return &runbookCleanupReservation{service: s, ctx: ctx, ownerID: ownerID, entries: snapshot}
}

func (s *runbookIPCService) CancelOwner(ctx context.Context, ownerID, eventID string, cutoff *time.Time, sessionIDs ...string) {
	commit, _ := s.PrepareOwnerCleanup(ctx, ownerID, eventID, cutoff)
	commit(sessionIDs...)
}

// Shutdown cancels every live run and clears the registry; durable run history is untouched.
func (s *runbookIPCService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	snapshot := s.runs
	s.runs = make(map[runbookRunKey]*runbookRunEntry)
	s.mu.Unlock()
	for key, entry := range snapshot {
		entry.runs.Cancel(ctx, key.ownerID, key.runID)
	}
	return nil
}

func runbookRunTerminal(status runbook.RunStatus) bool {
	return status == runbook.RunSuccess || status == runbook.RunFailed || status == runbook.RunCanceled
}

func runbookIPCError(err error) error {
	if err == nil {
		return nil
	}
	code := ipc.CodeInternal
	message := err.Error()
	switch {
	case errors.Is(err, runbook.ErrInvalid):
		code = ipc.CodeBadParam
	case errors.Is(err, runbook.ErrNotFound):
		code = ipc.CodeNotFound
	case errors.Is(err, runbook.ErrConflict), errors.Is(err, errRunbookActiveRunLimit):
		code = ipc.Code("conflict")
	case errors.Is(err, runbook.ErrForbidden):
		code = ipc.CodeForbidden
	case errors.Is(err, runbook.ErrStepConfirmationRequired), errors.Is(err, runbook.ErrProductionConfirmationNeeded):
		code = ipc.CodeNeedsConfirm
	default:
		return execBatchIPCError(err)
	}
	return ipc.WrapError(code, message, err)
}
