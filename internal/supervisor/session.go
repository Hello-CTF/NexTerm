package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/pty"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

type Session struct {
	supervisor *Supervisor
	entry      registryEntry
	identity   Identity
	attempt    string

	pty       *pty.Session
	recording *os.File

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

const (
	inputQueueSlots  = 16
	maxInputChunkLen = 256 * 1024
)

func newSession(supervisor *Supervisor, options CreateOptions, id string, ptySession *pty.Session, recording *os.File) *Session {
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
	return &Session{
		supervisor:  supervisor,
		entry:       entry,
		identity:    Identity{CreatedAt: now, Incarnation: incarnation},
		attempt:     options.Attempt,
		pty:         ptySession,
		recording:   recording,
		attachments: make(map[*Attachment]struct{}),
		inputQueue:  make(chan []byte, inputQueueSlots),
		inputDone:   make(chan struct{}),
		deadCh:      make(chan struct{}),
	}
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
		ID:          s.entry.ID,
		CreatedAt:   s.entry.CreatedAt,
		Incarnation: s.identity.Incarnation,
		Cols:        s.entry.Cols,
		Rows:        s.entry.Rows,
		Dead:        s.dead,
		ExitCode:    s.entry.ExitCode,
		Signal:      s.entry.Signal,
	}
	if s.entry.FinishedAt != nil {
		finished := *s.entry.FinishedAt
		info.FinishedAt = &finished
	}
	return info
}

func (s *Session) Write(p []byte) (int, error) {
	return s.writeInput(p, true)
}

func (s *Session) tryWrite(p []byte) (int, error) {
	return s.writeInput(p, false)
}

func (s *Session) writeInput(p []byte, wait bool) (int, error) {
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
		if err := s.enqueueInput(bytes.Clone(chunk), wait); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (s *Session) enqueueInput(chunk []byte, wait bool) error {
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

func (s *Session) attach() (*Attachment, error) {
	replay, err := openRecording(recordingPath(s.supervisor.stateDir, s.entry.ID))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	attachment := &Attachment{
		session: s,
		replay:  replay,
		notify:  make(chan struct{}, 1),
		ctx:     ctx,
		cancel:  cancel,
	}
	s.mu.Lock()
	s.attachments[attachment] = struct{}{}
	s.mu.Unlock()
	return attachment, nil
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

func (s *Session) record(chunk []byte) {
	if _, err := s.recording.Write(chunk); err != nil {
		s.setRecErr(fmt.Errorf("%w: write supervisor recording: %v", ErrUnavailable, err))
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
		persistErr := writeEntry(sessionsRoot(s.supervisor.stateDir), s.entry)
		s.mu.Unlock()
		closeErr := s.recording.Close()
		if errors.Is(closeErr, os.ErrClosed) {
			closeErr = nil
		}
		close(s.deadCh)
		s.stopInput()
		s.notifyAll()
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

func (s *Session) closePTY() {
	s.markKilled()
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
