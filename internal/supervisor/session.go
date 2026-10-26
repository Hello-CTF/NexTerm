package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type Session struct {
	supervisor *Supervisor
	entry      registryEntry
	identity   Identity
	attempt    string

	pty     *pty.Session
	history *history

	// exec is set for one-shot non-PTY sessions; pty and recording are nil.
	exec *execState

	mu          sync.RWMutex
	dead        bool
	exitErr     error
	killed      bool
	recErr      error
	inputErr    error
	attachments map[*Attachment]struct{}

	inputQueue    chan []byte
	inputDone     chan struct{}
	inputDoneOnce sync.Once

	deadCh     chan struct{}
	finishOnce sync.Once

	shellCleanup func()

	versionsMu sync.Mutex
}

// execState holds the process of a non-PTY session. drained tracks the two
// pipe consumers: cmd.Wait runs only after both consumers finish, as os/exec
// requires.
type execState struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	drained sync.WaitGroup
}

const (
	inputQueueSlots  = 16
	maxInputChunkLen = 256 * 1024
)

func newSession(supervisor *Supervisor, options CreateOptions, id string, ptySession *pty.Session, recording *history) *Session {
	now := time.Now()
	incarnation := ids.New()
	entry := registryEntry{
		ID:          id,
		CreatedAt:   now,
		Incarnation: incarnation,
		Attempt:     options.Attempt,
		Cols:        options.Cols,
		Rows:        options.Rows,
		Command:     options.Command,
		Dir:         options.Dir,
		Running:     true,
	}
	if recording != nil {
		entry.HistoryLimit = recording.limit
	}
	return &Session{
		supervisor:  supervisor,
		entry:       entry,
		identity:    Identity{CreatedAt: now, Incarnation: incarnation},
		attempt:     options.Attempt,
		pty:         ptySession,
		history:     recording,
		attachments: make(map[*Attachment]struct{}),
		inputQueue:  make(chan []byte, inputQueueSlots),
		inputDone:   make(chan struct{}),
		deadCh:      make(chan struct{}),
	}
}

func newExecSession(supervisor *Supervisor, options CreateOptions, id string, state *execState) *Session {
	session := newSession(supervisor, options, id, nil, nil)
	session.exec = state
	state.drained.Add(2)
	return session
}

func newRecoveredSession(supervisor *Supervisor, entry registryEntry) *Session {
	now := time.Now()
	if entry.FinishedAt == nil {
		entry.FinishedAt = &now
	}
	session := &Session{
		supervisor:  supervisor,
		entry:       entry,
		identity:    Identity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation},
		attempt:     entry.Attempt,
		history:     loadHistory(sessionDir(supervisor.stateDir, entry.ID), recoveredHistoryLimit(entry.HistoryLimit)),
		attachments: make(map[*Attachment]struct{}),
		inputQueue:  make(chan []byte, inputQueueSlots),
		inputDone:   make(chan struct{}),
		deadCh:      make(chan struct{}),
		dead:        true,
	}
	close(session.inputDone)
	if entry.ExitCode != nil || entry.Signal != "" {
		code := 0
		if entry.ExitCode != nil {
			code = *entry.ExitCode
		}
		session.exitErr = &base.ExitError{Code: code, Signal: entry.Signal}
	}
	close(session.deadCh)
	return session
}

func (s *Session) ID() string {
	return s.entry.ID
}

func (s *Session) Identity() Identity {
	return s.identity
}

func (s *Session) Attempt() string {
	return s.attempt
}

func (s *Session) Info() Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.infoLocked()
}

func (s *Session) infoLocked() Info {
	info := Info{
		ID:               s.entry.ID,
		CreatedAt:        s.entry.CreatedAt,
		Incarnation:      s.identity.Incarnation,
		Generation:       s.supervisor.generation,
		Title:            s.entry.Title,
		MetadataRevision: s.entry.MetadataRevision,
		Cols:             s.entry.Cols,
		Rows:             s.entry.Rows,
		Dead:             s.dead,
		ExitCode:         s.entry.ExitCode,
		Signal:           s.entry.Signal,
	}
	if s.entry.FinishedAt != nil {
		finished := *s.entry.FinishedAt
		info.FinishedAt = &finished
	}
	s.historyInfo(&info)
	return info
}

// historyInfo fills the recorded output bounds of info.
func (s *Session) historyInfo(info *Info) {
	if s.history == nil {
		return
	}
	info.HistoryBase, info.HistoryNext = s.history.bounds()
	info.HistoryLimit = s.history.limit
	info.HistoryTruncated = info.HistoryBase > 0
}

func recoveredHistoryLimit(limit uint64) uint64 {
	if validHistoryLimit(limit) != nil {
		return DefaultHistoryLimit
	}
	return limit
}

func (s *Session) Write(p []byte) (int, error) {
	return s.writeInput(p, true)
}

func (s *Session) writeInput(p []byte, wait bool) (int, error) {
	return s.writeInputCtx(context.Background(), p, wait)
}

// writeInputCtx is writeInput whose blocking enqueue also gives up when ctx
// ends, so a stalled consumer cannot pin a detached connection's writer.
func (s *Session) writeInputCtx(ctx context.Context, p []byte, wait bool) (int, error) {
	if s.exec != nil {
		return 0, fmt.Errorf("%w: non-PTY exec sessions take no input", ErrUnsupported)
	}
	s.mu.RLock()
	dead := s.dead
	s.mu.RUnlock()
	if dead {
		return 0, fmt.Errorf("%w: %s", ErrExited, s.entry.ID)
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxInputChunkLen {
			chunk = chunk[:maxInputChunkLen]
		}
		if err := s.enqueueInput(ctx, bytes.Clone(chunk), wait); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *Session) enqueueInput(ctx context.Context, chunk []byte, wait bool) error {
	select {
	case <-s.inputDone:
		return s.inputClosedError()
	default:
	}
	if !wait {
		select {
		case s.inputQueue <- chunk:
			return nil
		case <-s.inputDone:
			return s.inputClosedError()
		default:
			return fmt.Errorf("%w: input backlog is full", ErrUnavailable)
		}
	}
	select {
	case s.inputQueue <- chunk:
		return nil
	case <-s.inputDone:
		return s.inputClosedError()
	case <-ctx.Done():
		return ErrClosed
	}
}

func (s *Session) inputClosedError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.dead {
		return fmt.Errorf("%w: %s", ErrExited, s.entry.ID)
	}
	if s.inputErr != nil {
		return fmt.Errorf("%w: session input failed: %v", ErrUnavailable, s.inputErr)
	}
	return fmt.Errorf("%w: session input is closed", ErrUnavailable)
}

func (s *Session) inputPump() {
	defer s.supervisor.sessionDone()
	for {
		select {
		case chunk := <-s.inputQueue:
			if err := s.writePTY(chunk); err != nil {
				s.mu.Lock()
				if s.inputErr == nil {
					s.inputErr = err
				}
				s.mu.Unlock()
				s.stopInput()
				return
			}
		case <-s.inputDone:
			return
		}
	}
}

func (s *Session) writePTY(chunk []byte) error {
	for len(chunk) > 0 {
		count, err := s.pty.Write(chunk)
		if err != nil {
			return err
		}
		chunk = chunk[count:]
	}
	return nil
}

func (s *Session) stopInput() {
	s.inputDoneOnce.Do(func() {
		close(s.inputDone)
	})
}

func (s *Session) Resize(ctx context.Context, cols, rows uint32) error {
	if err := validateSize(cols, rows); err != nil {
		return err
	}
	s.mu.RLock()
	dead := s.dead
	s.mu.RUnlock()
	if dead {
		return fmt.Errorf("%w: %s", ErrExited, s.entry.ID)
	}
	if err := s.pty.Resize(ctx, cols, rows); err != nil {
		return err
	}
	s.history.noteResize(rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry.Cols = cols
	s.entry.Rows = rows
	_ = writeEntry(sessionsRoot(s.supervisor.stateDir), s.entry)
	return nil
}

func (s *Session) Wait(ctx context.Context) error {
	select {
	case <-s.deadCh:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.exitErr
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s *Session) attach(options AttachOptions) (*Attachment, error) {
	if s.exec != nil {
		return nil, fmt.Errorf("%w: non-PTY exec sessions are not attachable", ErrUnsupported)
	}
	if err := s.history.usable(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	var replay ReplayInfo
	var err error
	for attempt := 0; ; attempt++ {
		replay, err = s.planReplay(options)
		if !errors.Is(err, errHistoryEvicted) {
			break
		}
		if attempt == 3 {
			return nil, fmt.Errorf("%w: recorded output is evicted faster than it can be replayed", ErrUnavailable)
		}
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	attachment := &Attachment{
		session:   s,
		replay:    s.history.newReader(replay.ReplayFrom),
		nextSeq:   replay.ReplayFrom,
		replayEnd: replay.HistoryNext,
		result:    replay,
		notify:    make(chan struct{}, 1),
		ctx:       ctx,
		cancel:    cancel,
	}
	s.mu.Lock()
	s.attachments[attachment] = struct{}{}
	s.mu.Unlock()
	return attachment, nil
}

// planReplay chooses where an attach starts streaming. A resume (no Tail)
// starts at the requested offset, or at the first safe boundary at or after
// the history base when that offset was evicted (Truncated). A tail attach
// displays from a safe boundary within the last Tail bytes; with Retain it
// streams from the earlier requested offset (clamped to the base) so the
// caller can record the bytes it does not display.
//
// A tail starts at a line boundary, so a shell's scrollback renders as it
// would after a full replay. The prelude restores the screen buffer, input
// modes, origin mode, character sets and (on the alternate screen, reset by
// every resize) the scroll region in effect at the tail,
// including modes set before the history base (see terminalModes); SGR is not
// restored. Screen content drawn before the tail by a full-screen program
// with cursor addressing is not restored until the program repaints, which
// most do on their next update or on SIGWINCH. A recovered session whose
// segment lacks a persisted mode snapshot starts that segment from defaults.
func (s *Session) planReplay(options AttachOptions) (ReplayInfo, error) {
	base, next := s.history.bounds()
	if options.Tail == 0 && options.Offset > next {
		return ReplayInfo{}, fmt.Errorf("%w: attach offset %d beyond supervisor recording size %d", ErrInvalidInput, options.Offset, next)
	}
	if options.Retain && options.Offset > next {
		// A retained offset is the caller's bookkeeping (a transcript
		// offset), which can run ahead of the recording, for example after
		// a host crash lost unsynced output. Nothing beyond next can be
		// streamed; the caller sees ReplayFrom and HistoryNext and corrects
		// its offset.
		options.Offset = next
	}
	replay := ReplayInfo{HistoryBase: base, HistoryNext: next}
	if options.Tail == 0 {
		replay.ReplayFrom = options.Offset
		if options.Offset < base {
			start, err := s.history.safeStartFrom(base)
			if err != nil {
				return ReplayInfo{}, err
			}
			replay.ReplayFrom = start
			replay.Truncated = true
		}
		replay.DisplayFrom = replay.ReplayFrom
		return replay, nil
	}
	low := base
	if next-base > options.Tail {
		low = next - options.Tail
	}
	display, err := s.history.safeStartFrom(low)
	if err != nil {
		return ReplayInfo{}, err
	}
	replay.DisplayFrom = display
	replay.ReplayFrom = display
	if options.Retain && options.Offset < display {
		replay.ReplayFrom = max(options.Offset, base)
		replay.Truncated = options.Offset < base
	}
	if display > 0 {
		prelude, err := s.history.modesPreludeAt(display)
		if err != nil {
			return ReplayInfo{}, err
		}
		replay.Prelude = prelude
	}
	return replay, nil
}

// readHistory returns retained bytes ending at before, at most maxBytes, with
// the start moved forward to a safe boundary unless it is offset zero.
func (s *Session) readHistory(before uint64, maxBytes int) (HistoryChunk, error) {
	if s.exec != nil {
		return HistoryChunk{}, fmt.Errorf("%w: non-PTY exec sessions have no history", ErrUnsupported)
	}
	if err := s.history.usable(); err != nil {
		return HistoryChunk{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if maxBytes <= 0 || maxBytes > MaxHistoryRead {
		maxBytes = MaxHistoryRead
	}
	for attempt := 0; attempt < 4; attempt++ {
		base, next := s.history.bounds()
		if before > next {
			return HistoryChunk{}, fmt.Errorf("%w: history read end %d beyond supervisor recording size %d", ErrInvalidInput, before, next)
		}
		chunk := HistoryChunk{HistoryBase: base, HistoryNext: next, Start: before, End: before}
		if before <= base {
			chunk.Truncated = base > 0
			return chunk, nil
		}
		start := base
		if before-base > uint64(maxBytes) {
			start = before - uint64(maxBytes)
		}
		data := make([]byte, before-start)
		count, ok, err := s.history.readAt(start, data)
		if err != nil {
			return HistoryChunk{}, err
		}
		if !ok || count < len(data) {
			continue
		}
		skip := 0
		if start > 0 {
			skip = safeReplayStart(data)
			if skip == len(data) && start > base {
				// No safe boundary at all: return the window unadjusted so
				// a reader walking backwards keeps making progress.
				skip = 0
			}
		}
		chunk.Start = start + uint64(skip)
		chunk.Data = data[skip:]
		// At the base, the partial sequence before the first safe boundary
		// is dropped with the output before the base.
		chunk.Truncated = start == base && base > 0
		return chunk, nil
	}
	return HistoryChunk{}, fmt.Errorf("%w: recorded output is evicted faster than it can be read", ErrUnavailable)
}

func (s *Session) removeAttachment(attachment *Attachment) {
	s.mu.Lock()
	delete(s.attachments, attachment)
	s.mu.Unlock()
}

func (s *Session) detachAttachments() {
	s.mu.RLock()
	attachments := make([]*Attachment, 0, len(s.attachments))
	for attachment := range s.attachments {
		attachments = append(attachments, attachment)
	}
	s.mu.RUnlock()
	for _, attachment := range attachments {
		_ = attachment.Detach()
	}
}

func (s *Session) status() (dead bool, recErr error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dead, s.recErr
}

func (s *Session) notifyAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for attachment := range s.attachments {
		select {
		case attachment.notify <- struct{}{}:
		default:
		}
	}
}

func (s *Session) pump() {
	defer s.supervisor.sessionDone()
	buffer := make([]byte, 32*1024)
	for {
		count, err := s.pty.Read(buffer)
		if count > 0 {
			s.record(buffer[:count])
		}
		if err != nil {
			break
		}
	}
	s.finish(s.pty.Wait(context.Background()))
}

// execWait reaps a non-PTY process after its pipe consumers finish. Normal
// consumers drain both pipes before Wait; abort closes them to unblock the
// consumers without waiting for descendants that inherited the write ends.
func (s *Session) execWait() {
	defer s.supervisor.sessionDone()
	s.exec.drained.Wait()
	s.finish(execExitStatus(s.exec.cmd.Wait()))
}

// reapExec discards a non-PTY session's output and reaps the process on abort
// paths where no stream consumer exists to drain the pipes.
func (s *Session) reapExec() {
	s.drainExecPipes()
	s.execWait()
}

// drainExecPipes starts the two discard consumers that let execWait complete
// when no stream consumer will ever drain the process pipes.
func (s *Session) drainExecPipes() {
	go func() {
		_, _ = io.Copy(io.Discard, s.exec.stdout)
		s.exec.drained.Done()
	}()
	go func() {
		_, _ = io.Copy(io.Discard, s.exec.stderr)
		s.exec.drained.Done()
	}()
}

func execExitStatus(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &base.ExitError{Code: exitErr.ExitCode(), Signal: execSignalName(exitErr.ProcessState)}
	}
	return err
}

func (s *Session) record(chunk []byte) {
	if err := s.history.append(chunk); err != nil {
		s.setRecErr(err)
	}
	s.notifyAll()
}

func (s *Session) setRecErr(err error) {
	s.mu.Lock()
	if s.recErr == nil {
		s.recErr = err
	}
	s.mu.Unlock()
}

func (s *Session) finish(waitErr error) {
	s.finishOnce.Do(func() {
		defer s.cleanupShell()
		code, signal, exitErr := exitStatus(waitErr)
		s.mu.Lock()
		s.dead = true
		if exitErr == nil && s.killed {
			signal = killSignalName()
			if signal != "" {
				killedCode := -1
				code = &killedCode
				exitErr = &base.ExitError{Code: killedCode, Signal: signal}
			}
		}
		s.exitErr = exitErr
		s.entry.Running = false
		s.entry.ExitCode = code
		s.entry.Signal = signal
		now := time.Now()
		s.entry.FinishedAt = &now
		// Non-PTY sessions persist nothing: no registry entry, no recording,
		// and the session is dropped from the supervisor once it exits.
		var persistErr error
		if s.exec == nil {
			persistErr = writeEntry(sessionsRoot(s.supervisor.stateDir), s.entry)
		}
		s.mu.Unlock()
		var closeErr error
		if s.history != nil {
			closeErr = s.history.closeWriter()
		}
		close(s.deadCh)
		s.stopInput()
		s.notifyAll()
		if s.exec != nil {
			s.supervisor.dropSession(s.entry.ID)
		}
		s.supervisor.recordFinishError(errors.Join(persistErr, closeErr))
	})
}

func exitStatus(err error) (code *int, signal string, exitErr error) {
	var status *base.ExitError
	if errors.As(err, &status) {
		value := status.Code
		return &value, status.Signal, status
	}
	if err == nil {
		zero := 0
		return &zero, "", nil
	}
	return nil, "", nil
}

func (s *Session) closeProcess() {
	s.markKilled()
	if s.exec != nil {
		if s.exec.cmd.Process != nil {
			_ = s.exec.cmd.Process.Kill()
		}
		// A descendant can inherit the write ends, so closing the parent's read
		// ends is required to release the drainers and let cmd.Wait run.
		_ = s.exec.stdout.Close()
		_ = s.exec.stderr.Close()
		return
	}
	_ = s.pty.Close()
}

func (s *Session) cleanupShell() {
	if s.shellCleanup != nil {
		s.shellCleanup()
	}
}

func (s *Session) markKilled() {
	s.mu.Lock()
	s.killed = true
	s.mu.Unlock()
}

func (s *Session) Versions() (eventVersion, gridRevision uint64, err error) {
	s.versionsMu.Lock()
	defer s.versionsMu.Unlock()
	return s.readVersions()
}

func (s *Session) PersistVersions(eventVersion, gridRevision uint64) error {
	s.versionsMu.Lock()
	defer s.versionsMu.Unlock()
	currentEvent, currentGrid, err := s.readVersions()
	if err != nil {
		return err
	}
	eventVersion = max(eventVersion, currentEvent)
	gridRevision = max(gridRevision, currentGrid)
	data := []byte(strconv.FormatUint(eventVersion, 10) + " " + strconv.FormatUint(gridRevision, 10) + "\n")
	path := versionsPath(s.supervisor.stateDir, s.entry.ID)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return fmt.Errorf("write supervisor versions: %w", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("commit supervisor versions: %w", err)
	}
	return nil
}

func (s *Session) readVersions() (eventVersion, gridRevision uint64, err error) {
	data, err := os.ReadFile(versionsPath(s.supervisor.stateDir, s.entry.ID))
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read supervisor versions: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("parse supervisor versions: expected 2 fields, got %q", data)
	}
	eventVersion, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse supervisor event version: %w", err)
	}
	gridRevision, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse supervisor grid revision: %w", err)
	}
	return eventVersion, gridRevision, nil
}
