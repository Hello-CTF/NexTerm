package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Session struct {
	backend    *Backend
	expected   record
	log        *os.File
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	readMu     sync.Mutex
	writeMu    sync.Mutex
	versionsMu sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func newSession(backend *Backend, expected record, log *os.File) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		backend:  backend,
		expected: expected,
		log:      log,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

func (s *Session) Info() Info { return cloneInfo(s.expected.info) }

func (s *Session) Read(buffer []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.closed() {
		return 0, ErrClosed
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	nextStatusCheck := time.Now()
	for {
		count, err := s.log.Read(buffer)
		if count > 0 {
			return count, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			if s.closed() {
				return 0, ErrClosed
			}
			return 0, err
		}
		if !time.Now().Before(nextStatusCheck) {
			current, resolveErr := s.backend.resolve(s.ctx, s.expected.info.ID)
			if resolveErr != nil {
				if s.closed() {
					return 0, ErrClosed
				}
				if errors.Is(resolveErr, ErrNotFound) {
					return 0, io.EOF
				}
				return 0, resolveErr
			}
			if !sameIdentity(s.expected.info, current.info) || s.expected.windowID != current.windowID {
				return 0, fmt.Errorf("%w: %s", ErrIdentity, s.expected.info.ID)
			}
			if !current.info.Dead && !current.recordingLive {
				closing, closingErr := s.backend.recorderClosing(s.expected.info.ID)
				if closingErr != nil {
					return 0, closingErr
				}
				if !closing {
					return 0, fmt.Errorf("%w: tmux output recording stopped", ErrUnavailable)
				}
			}
			complete, completionErr := s.backend.recorderComplete(s.expected.info.ID)
			if completionErr != nil {
				return 0, completionErr
			}
			if complete {
				count, err = s.log.Read(buffer)
				if count > 0 {
					return count, nil
				}
				if err != nil && !errors.Is(err, io.EOF) {
					if s.closed() {
						return 0, ErrClosed
					}
					return 0, err
				}
				return 0, io.EOF
			}
			nextStatusCheck = time.Now().Add(s.backend.statusInterval)
		}
		timer := time.NewTimer(s.backend.pollInterval)
		select {
		case <-s.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return 0, ErrClosed
		case <-timer.C:
		}
	}
}

func (s *Session) Write(buffer []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed() {
		return 0, ErrClosed
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	operations, err := makeInputOperations(buffer)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, operation := range operations {
		current, err := s.current(s.ctx, true)
		if err != nil {
			return written, s.closedError(err)
		}
		args := []string{"send-keys", "-t", current.info.PaneID}
		if operation.literal {
			args = append(args, "-l", "--", string(operation.data))
		} else {
			args = append(args, "-H")
			for _, value := range operation.data {
				args = append(args, hexByte(value))
			}
		}
		if _, err := s.backend.run(s.ctx, args...); err != nil {
			return written, s.closedError(err)
		}
		written += operation.size
	}
	return written, nil
}

func (s *Session) Resize(ctx context.Context, cols, rows uint32) error {
	if s.closed() {
		return ErrClosed
	}
	if err := validateSize(cols, rows); err != nil {
		return err
	}
	opCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	current, err := s.current(opCtx, true)
	if err != nil {
		return s.closedError(err)
	}
	_, err = s.backend.run(opCtx, "resize-window", "-t", current.windowID,
		"-x", strconv.FormatUint(uint64(cols), 10),
		"-y", strconv.FormatUint(uint64(rows), 10))
	return s.closedError(err)
}

func (s *Session) Kill(ctx context.Context) error {
	if err := s.backend.kill(ctx, s.expected.info.ID, &s.expected); err != nil {
		return err
	}
	return s.Detach()
}

func (s *Session) Detach() error {
	s.closeOnce.Do(func() {
		s.cancel()
		close(s.done)
		s.closeErr = s.log.Close()
		if errors.Is(s.closeErr, os.ErrClosed) {
			s.closeErr = nil
		}
	})
	return s.closeErr
}

func (s *Session) Close() error { return s.Detach() }

// Versions returns the persisted per-tab event/grid version floor recorded
// by previous attachments of this durable identity. A missing floor (fresh
// identity, or a session created before floors existed) reads as zeroes.
func (s *Session) Versions() (eventVersion, gridRevision uint64, err error) {
	s.versionsMu.Lock()
	defer s.versionsMu.Unlock()
	return s.readVersions()
}

// PersistVersions records the given per-tab event/grid versions as the new
// floor. The floor never rewinds: concurrent attachments of the same
// identity may race, and recovery only needs a monotonic lower bound so
// reconnected clients never see versions they already observed.
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
	path := s.backend.versionsTempPath(s.expected.info.ID)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write durable versions: %w", err)
	}
	if err := os.Rename(path, s.backend.versionsPath(s.expected.info.ID)); err != nil {
		return fmt.Errorf("commit durable versions: %w", err)
	}
	return nil
}

func (s *Session) readVersions() (eventVersion, gridRevision uint64, err error) {
	data, err := os.ReadFile(s.backend.versionsPath(s.expected.info.ID))
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read durable versions: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("parse durable versions: expected 2 fields, got %q", data)
	}
	eventVersion, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse durable event version: %w", err)
	}
	gridRevision, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse durable grid revision: %w", err)
	}
	return eventVersion, gridRevision, nil
}

func (s *Session) current(ctx context.Context, requireRunning bool) (record, error) {
	current, err := s.backend.resolve(ctx, s.expected.info.ID)
	if err != nil {
		return record{}, err
	}
	if !sameIdentity(s.expected.info, current.info) || s.expected.windowID != current.windowID {
		return record{}, fmt.Errorf("%w: %s", ErrIdentity, s.expected.info.ID)
	}
	if requireRunning && current.info.Dead {
		return record{}, fmt.Errorf("%w: %s", ErrExited, s.expected.info.ID)
	}
	return current, nil
}

func (s *Session) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Session) closedError(err error) error {
	if err != nil && s.closed() {
		return errors.Join(ErrClosed, err)
	}
	return err
}
