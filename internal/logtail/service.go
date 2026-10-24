package logtail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	maxPendingCancels         = 128
	maxPendingCancelsPerOwner = 16
	maxFinishedRuns           = 128
	terminalEventTimeout      = time.Second
)

type Service struct {
	resolver     Resolver
	now          func() time.Time
	drainTimeout time.Duration

	mu       sync.Mutex
	runs     map[string]*activeRun
	pending  map[pendingCancelKey]struct{}
	finished map[string]string
	closed   bool
}

type activeRun struct {
	id     string
	owner  string
	ctx    context.Context
	cancel context.CancelFunc
	sink   Sink
	done   chan struct{}
	now    func() time.Time

	emitMu   sync.Mutex
	sequence uint64
}

type pendingCancelKey struct {
	owner string
	run   string
}

func New(config Config) *Service {
	return NewWithDrainTimeout(config, hub.DefaultDrainTimeout)
}

func NewWithDrainTimeout(config Config, drainTimeout time.Duration) *Service {
	now := config.Now
	if now == nil {
		now = time.Now
	}
	if drainTimeout <= 0 {
		drainTimeout = hub.DefaultDrainTimeout
	}
	return &Service{
		resolver: config.Resolver, now: now, drainTimeout: drainTimeout,
		runs: make(map[string]*activeRun), pending: make(map[pendingCancelKey]struct{}), finished: make(map[string]string),
	}
}

func (s *Service) Start(ctx context.Context, owner string, request Request, sink Sink) (StartResult, error) {
	concurrency, err := validateRequest(request)
	if err != nil {
		return StartResult{}, err
	}
	if s.resolver == nil || sink == nil {
		return StartResult{}, fmt.Errorf("%w: resolver and sink are required", ErrUnsupported)
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	run := &activeRun{id: request.RunID, owner: owner, ctx: runCtx, cancel: cancel, sink: sink, done: make(chan struct{}), now: s.now}
	key := pendingCancelKey{owner: owner, run: request.RunID}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return StartResult{}, ErrClosed
	}
	if existing := s.runs[request.RunID]; existing != nil {
		s.mu.Unlock()
		cancel()
		if existing.owner != owner {
			return StartResult{}, ErrForbidden
		}
		return StartResult{}, ErrConflict
	}
	if finishedOwner, exists := s.finished[request.RunID]; exists {
		if finishedOwner != owner {
			s.mu.Unlock()
			cancel()
			return StartResult{}, ErrForbidden
		}
		delete(s.finished, request.RunID)
	}
	s.runs[request.RunID] = run
	if _, exists := s.pending[key]; exists {
		delete(s.pending, key)
		cancel()
	}
	s.mu.Unlock()
	go s.run(run, request.Sources, concurrency)
	return StartResult{RunID: request.RunID}, nil
}

func (s *Service) Cancel(ctx context.Context, owner, runID string) (CancelResult, error) {
	if runID == "" || len(runID) > 128 || strings.TrimSpace(runID) != runID {
		return CancelResult{}, fmt.Errorf("%w: invalid runId", ErrInvalid)
	}
	key := pendingCancelKey{owner: owner, run: runID}
	s.mu.Lock()
	run := s.runs[runID]
	if run == nil {
		if finishedOwner, exists := s.finished[runID]; exists {
			s.mu.Unlock()
			if finishedOwner != owner {
				return CancelResult{}, ErrForbidden
			}
			return CancelResult{RunID: runID, Canceled: true}, nil
		}
		if _, exists := s.pending[key]; !exists {
			if len(s.pending) >= maxPendingCancels || s.pendingOwnerCountLocked(owner) >= maxPendingCancelsPerOwner {
				s.mu.Unlock()
				return CancelResult{}, ErrPendingLimit
			}
			s.pending[key] = struct{}{}
		}
		s.mu.Unlock()
		return CancelResult{RunID: runID, Canceled: true}, nil
	}
	if run.owner != owner {
		s.mu.Unlock()
		return CancelResult{}, ErrForbidden
	}
	run.cancel()
	done := run.done
	s.mu.Unlock()
	select {
	case <-done:
		return CancelResult{RunID: runID, Canceled: true}, nil
	case <-ctx.Done():
		return CancelResult{}, ctx.Err()
	}
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	runs := make([]*activeRun, 0, len(s.runs))
	for _, run := range s.runs {
		run.cancel()
		runs = append(runs, run)
	}
	s.mu.Unlock()
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) run(run *activeRun, sources []Source, concurrency int) {
	defer run.cancel()
	defer close(run.done)
	defer closeSink(run.sink, s.drainTimeout)
	defer func() {
		s.mu.Lock()
		if s.runs[run.id] == run {
			delete(s.runs, run.id)
			s.finished[run.id] = run.owner
			if len(s.finished) > maxFinishedRuns {
				for finishedID := range s.finished {
					if finishedID != run.id {
						delete(s.finished, finishedID)
						break
					}
				}
			}
		}
		s.mu.Unlock()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), terminalEventTimeout)
		defer cancel()
		run.emit(ctx, Event{Kind: EventDone})
	}()

	for _, source := range sources {
		if run.ctx.Err() != nil {
			break
		}
		if !run.emitStatus(run.ctx, source, StatusQueued, "") {
			return
		}
	}
	semaphore := make(chan struct{}, concurrency)
	var workers sync.WaitGroup
	for _, source := range sources {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.executeSource(run, source, semaphore)
		}()
	}
	workers.Wait()
}

func closeSink(sink Sink, drainTimeout time.Duration) error {
	if graceful, ok := sink.(interface{ CloseGracefully() error }); ok {
		err := graceful.CloseGracefully()
		time.AfterFunc(drainTimeout, func() { _ = sink.Close() })
		return err
	}
	return sink.Close()
}

func (s *Service) executeSource(run *activeRun, source Source, semaphore chan struct{}) {
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	case <-run.ctx.Done():
		run.emitTerminalStatus(run, source, StatusCanceled)
		return
	}
	if run.ctx.Err() != nil {
		run.emitTerminalStatus(run, source, StatusCanceled)
		return
	}
	if !run.emitStatus(run.ctx, source, StatusStarting, "") {
		run.emitTerminalStatus(run, source, StatusCanceled)
		return
	}
	command, err := sourceCommand(source)
	if err != nil {
		run.emitStatus(run.ctx, source, StatusFailed, err.Error())
		return
	}
	target, err := s.resolver.Resolve(run.ctx, source.SessionID)
	if err != nil {
		run.emitTerminalStatus(run, source, StatusCanceled)
		if run.ctx.Err() == nil {
			run.emitStatus(run.ctx, source, StatusFailed, err.Error())
		}
		return
	}
	if target.SessionID != "" {
		source.SessionID = target.SessionID
	}
	source.SessionName = target.SessionName
	if target.Transport == nil || !target.Transport.IsAlive() {
		run.emitStatus(run.ctx, source, StatusFailed, "连接已断开，请重新连接后再试")
		return
	}
	opener, ok := target.Transport.(base.ExecStreamTransport)
	if !ok {
		run.emitStatus(run.ctx, source, StatusFailed, "当前连接不支持流式执行，无法跟随日志")
		return
	}
	generation := target.Generation
	if generation == 0 {
		generation = target.Transport.Generation()
	}
	channel, err := opener.OpenExec(run.ctx, command, base.ExecOptions{ExpectedGeneration: generation})
	if err != nil {
		run.emitTerminalStatus(run, source, StatusCanceled)
		if run.ctx.Err() == nil {
			run.emitStatus(run.ctx, source, StatusFailed, err.Error())
		}
		return
	}
	defer channel.Close()
	ordered, ok := channel.(base.OrderedOutput)
	if !ok {
		run.emitStatus(run.ctx, source, StatusFailed, "执行通道不支持有序输出，无法跟随日志")
		return
	}
	if !run.emitStatus(run.ctx, source, StatusRunning, "") {
		run.emitTerminalStatus(run, source, StatusCanceled)
		return
	}
	assemblers := [2]*lineAssembler{
		newLineAssembler(func(text string) error {
			return run.emitLine(run.ctx, source, text, false)
		}),
		newLineAssembler(func(text string) error {
			return run.emitLine(run.ctx, source, text, true)
		}),
	}
	var streamErr error
	for {
		output, nextErr := ordered.NextOutput(run.ctx)
		if len(output.Data) > 0 {
			index := 0
			if output.Stderr {
				index = 1
			}
			if writeErr := assemblers[index].Write(output.Data); writeErr != nil {
				streamErr = writeErr
				break
			}
		}
		if nextErr != nil {
			if !errors.Is(nextErr, io.EOF) {
				streamErr = nextErr
			}
			break
		}
		if output.Err != nil {
			streamErr = output.Err
			break
		}
	}
	for _, assembler := range assemblers {
		if err := assembler.Flush(); err != nil && streamErr == nil {
			streamErr = err
		}
	}
	waitErr := channel.Wait(run.ctx)
	if run.ctx.Err() != nil {
		run.emitTerminalStatus(run, source, StatusCanceled)
		return
	}
	if streamErr != nil {
		run.emitStatus(run.ctx, source, StatusFailed, streamErr.Error())
		return
	}
	if waitErr != nil {
		run.emitStatus(run.ctx, source, StatusFailed, waitErr.Error())
		return
	}
	run.emitStatus(run.ctx, source, StatusExited, "")
}

func (r *activeRun) emitTerminalStatus(run *activeRun, source Source, status SourceStatus) {
	if run.ctx.Err() == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminalEventTimeout)
	defer cancel()
	r.emitStatus(ctx, source, status, "")
}

func (r *activeRun) emitStatus(ctx context.Context, source Source, status SourceStatus, message string) bool {
	return r.emit(ctx, Event{
		Kind: EventStatus, SourceID: source.SourceID, SessionID: source.SessionID, SessionName: source.SessionName,
		Status: status, Error: message,
	}) == nil
}

func (r *activeRun) emitLine(ctx context.Context, source Source, text string, stderr bool) error {
	return r.emit(ctx, Event{
		Kind: EventLine, SourceID: source.SourceID, SessionID: source.SessionID, SessionName: source.SessionName,
		Text: text, Stderr: stderr,
	})
}

func (r *activeRun) emit(ctx context.Context, event Event) error {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	r.sequence++
	event.RunID = r.id
	event.Sequence = r.sequence
	event.TimeMS = r.now().UnixMilli()
	if err := r.sink.Send(ctx, event); err != nil {
		r.cancel()
		return err
	}
	return nil
}

func (s *Service) pendingOwnerCountLocked(owner string) int {
	count := 0
	for key := range s.pending {
		if key.owner == owner {
			count++
		}
	}
	return count
}

type lineAssembler struct {
	pending []byte
	emit    func(string) error
}

func newLineAssembler(emit func(string) error) *lineAssembler {
	return &lineAssembler{emit: emit}
}

func (a *lineAssembler) Write(data []byte) error {
	a.pending = append(a.pending, data...)
	for {
		index := bytes.IndexByte(a.pending, '\n')
		if index < 0 {
			break
		}
		line := a.pending[:index]
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if err := a.emit(strings.ToValidUTF8(string(line), "�")); err != nil {
			return err
		}
		a.pending = a.pending[index+1:]
	}
	for len(a.pending) >= MaxEventLineSize {
		if err := a.emit(strings.ToValidUTF8(string(a.pending[:MaxEventLineSize]), "�")); err != nil {
			return err
		}
		a.pending = a.pending[MaxEventLineSize:]
	}
	return nil
}

func (a *lineAssembler) Flush() error {
	if len(a.pending) == 0 {
		return nil
	}
	line := strings.TrimSuffix(strings.ToValidUTF8(string(a.pending), "�"), "\r")
	a.pending = nil
	return a.emit(line)
}
