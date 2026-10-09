package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/pty"
)

var ptyStart = pty.Start

type Supervisor struct {
	stateDir       string
	commandTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	sessions   map[string]*Session
	killed     map[string]struct{}
	closing    bool
	finishErrs []error

	killMu  sync.Mutex
	creates sync.WaitGroup
	pumps   sync.WaitGroup
}

func New(config Config) (*Supervisor, error) {
	if config.StateDir == "" {
		return nil, fmt.Errorf("%w: StateDir is required", ErrInvalidInput)
	}
	if config.CommandTimeout == 0 {
		config.CommandTimeout = defaultCommandTimeout
	}
	if config.CommandTimeout < 0 {
		return nil, fmt.Errorf("%w: negative command timeout", ErrInvalidInput)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil {
		return nil, fmt.Errorf("%w: state directory: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(stateDir); err != nil {
		return nil, fmt.Errorf("supervisor state directory: %w", err)
	}
	if err := ensurePrivateDir(sessionsRoot(stateDir)); err != nil {
		return nil, fmt.Errorf("supervisor sessions directory: %w", err)
	}
	entries, err := loadRegistry(stateDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := &Supervisor{
		stateDir:       stateDir,
		commandTimeout: config.CommandTimeout,
		ctx:            ctx,
		cancel:         cancel,
		sessions:       make(map[string]*Session),
		killed:         make(map[string]struct{}),
	}
	for _, entry := range entries {
		supervisor.sessions[entry.ID] = newRecoveredSession(supervisor, entry)
	}
	return supervisor, nil
}

func (s *Supervisor) StateDir() string {
	return s.stateDir
}

func (s *Supervisor) Create(ctx context.Context, options CreateOptions) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, id, err := normalizeCreate(options)
	if err != nil {
		return nil, err
	}
	s.killMu.Lock()
	defer s.killMu.Unlock()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if _, exists := s.sessions[id]; exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
	}
	delete(s.killed, id)
	s.creates.Add(1)
	s.mu.Unlock()
	defer s.creates.Done()

	dir := sessionDir(s.stateDir, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: artifacts for %s", ErrAlreadyExists, id)
		}
		return nil, fmt.Errorf("create supervisor session directory: %w", err)
	}
	abort := func(cause error) (*Session, error) {
		if cleanupErr := removeRegistryArtifacts(sessionsRoot(s.stateDir), id); cleanupErr != nil {
			return nil, errors.Join(cause, cleanupErr)
		}
		return nil, cause
	}
	recording, err := os.OpenFile(recordingPath(s.stateDir, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return abort(fmt.Errorf("create supervisor recording: %w", err))
	}
	launch, wrapped := wrapShellIntegration(options.Command, options.Env)
	command, commandEnv := options.Command, options.Env
	if wrapped {
		command, commandEnv = launch.command, launch.env
	}
	ptySession, err := ptyStart(s.ctx, pty.Config{
		Path: command[0],
		Args: command[1:],
		Dir:  options.Dir,
		Env:  mergedEnv(commandEnv),
		Cols: options.Cols,
		Rows: options.Rows,
		ID:   id,
	})
	if err != nil {
		if wrapped {
			launch.cleanup()
		}
		_ = recording.Close()
		return abort(err)
	}
	session := newSession(s, options, id, ptySession, recording)
	if wrapped {
		session.shellCleanup = launch.cleanup
	}
	if err := writeEntry(sessionsRoot(s.stateDir), session.entry); err != nil {
		session.closePTY()
		session.cleanupShell()
		_ = recording.Close()
		return abort(err)
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		session.closePTY()
		session.cleanupShell()
		_ = recording.Close()
		return abort(ErrClosed)
	}
	s.sessions[id] = session
	s.pumps.Add(2)
	s.mu.Unlock()
	go session.pump()
	go session.inputPump()
	return session, nil
}

func (s *Supervisor) Attach(ctx context.Context, id string) (*Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := s.resolveSession(id)
	if err != nil {
		return nil, err
	}
	return session.attach()
}

func (s *Supervisor) resolveSession(id string) (*Session, error) {
	if !ids.Valid(id) {
		return nil, fmt.Errorf("%w: invalid supervisor session ID", ErrInvalidInput)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, exists := s.sessions[id]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return session, nil
}

func (s *Supervisor) List(ctx context.Context) ([]Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	infos := make([]Info, 0, len(sessions))
	for _, session := range sessions {
		infos = append(infos, session.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

func (s *Supervisor) Kill(ctx context.Context, id string) error {
	return s.kill(ctx, id, nil)
}

func (s *Supervisor) kill(ctx context.Context, id string, expected *Identity) error {
	if !ids.Valid(id) {
		return fmt.Errorf("%w: invalid supervisor session ID", ErrInvalidInput)
	}
	s.killMu.Lock()
	defer s.killMu.Unlock()
	s.mu.Lock()
	session, exists := s.sessions[id]
	_, killed := s.killed[id]
	s.mu.Unlock()
	if !exists {
		if killed {
			return nil
		}
		present, err := s.artifactsPresent(id)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		if expected != nil {
			entry, err := readEntry(sessionsRoot(s.stateDir), id)
			if err != nil || !sameIdentity(*expected, Identity{CreatedAt: entry.CreatedAt, Incarnation: entry.Incarnation}) {
				return fmt.Errorf("%w: leftover artifacts for %s cannot be proven to belong to the expected identity", ErrIdentity, id)
			}
		}
		if err := removeRegistryArtifacts(sessionsRoot(s.stateDir), id); err != nil {
			return fmt.Errorf("clean up leftover supervisor artifacts for %s: %w", id, err)
		}
		s.mu.Lock()
		s.killed[id] = struct{}{}
		s.mu.Unlock()
		return nil
	}
	if expected != nil && !sameIdentity(*expected, session.Identity()) {
		return fmt.Errorf("%w: %s", ErrIdentity, id)
	}
	if err := session.killAndWait(ctx, s.commandTimeout); err != nil {
		return err
	}
	session.detachAttachments()
	if err := removeRegistryArtifacts(sessionsRoot(s.stateDir), id); err != nil {
		return fmt.Errorf("supervisor session killed but artifacts remain: %w", err)
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.killed[id] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) killByAttempt(ctx context.Context, id, attempt string) error {
	if !ids.Valid(id) {
		return fmt.Errorf("%w: invalid supervisor session ID", ErrInvalidInput)
	}
	if !ids.Valid(attempt) {
		return fmt.Errorf("%w: invalid supervisor create attempt", ErrInvalidInput)
	}
	s.killMu.Lock()
	defer s.killMu.Unlock()
	s.mu.Lock()
	session, exists := s.sessions[id]
	_, killed := s.killed[id]
	s.mu.Unlock()
	if !exists {
		if killed {
			return nil
		}
		present, err := s.artifactsPresent(id)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		entry, err := readEntry(sessionsRoot(s.stateDir), id)
		if err != nil || entry.Attempt != attempt {
			return fmt.Errorf("%w: leftover artifacts for %s do not match the create attempt", ErrIdentity, id)
		}
		if err := removeRegistryArtifacts(sessionsRoot(s.stateDir), id); err != nil {
			return fmt.Errorf("clean up leftover supervisor artifacts for %s: %w", id, err)
		}
		s.mu.Lock()
		s.killed[id] = struct{}{}
		s.mu.Unlock()
		return nil
	}
	if session.Attempt() != attempt {
		return fmt.Errorf("%w: session %s belongs to a different create attempt", ErrIdentity, id)
	}
	if err := session.killAndWait(ctx, s.commandTimeout); err != nil {
		return err
	}
	session.detachAttachments()
	if err := removeRegistryArtifacts(sessionsRoot(s.stateDir), id); err != nil {
		return fmt.Errorf("supervisor session killed but artifacts remain: %w", err)
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.killed[id] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) artifactsPresent(id string) (bool, error) {
	if !ids.Valid(id) {
		return false, fmt.Errorf("%w: invalid supervisor session ID", ErrInvalidInput)
	}
	for _, path := range []string{sessionDir(s.stateDir, id), tombstonePath(s.stateDir, id)} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect supervisor artifacts for %s: %w", id, err)
		}
	}
	return false, nil
}

func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closing {
		errs := s.finishErrs
		s.mu.Unlock()
		return errors.Join(errs...)
	}
	s.closing = true
	s.mu.Unlock()

	s.creates.Wait()
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	for _, session := range sessions {
		session.markKilled()
	}
	s.cancel()
	var errs []error
	for _, session := range sessions {
		select {
		case <-session.deadCh:
		case <-time.After(s.commandTimeout):
			errs = append(errs, fmt.Errorf("%w: session %s did not stop", ErrUnavailable, session.ID()))
		}
	}
	pumpsDone := make(chan struct{})
	go func() {
		s.pumps.Wait()
		close(pumpsDone)
	}()
	select {
	case <-pumpsDone:
	case <-time.After(s.commandTimeout):
		errs = append(errs, fmt.Errorf("%w: supervisor pumps did not stop", ErrUnavailable))
	}
	s.mu.Lock()
	errs = append(errs, s.finishErrs...)
	s.mu.Unlock()
	return errors.Join(errs...)
}

func (s *Supervisor) sessionDone() {
	s.pumps.Done()
}

func (s *Supervisor) recordFinishError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.finishErrs = append(s.finishErrs, err)
	s.mu.Unlock()
}

func (s *Session) killAndWait(ctx context.Context, timeout time.Duration) error {
	s.mu.RLock()
	dead := s.dead
	s.mu.RUnlock()
	if !dead {
		s.closePTY()
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-s.deadCh:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("%w: session %s did not exit: %v", ErrUnavailable, s.ID(), context.Cause(waitCtx))
	}
}
