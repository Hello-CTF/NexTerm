package pty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

const maxDimension = 1024

type Config struct {
	Path       string
	Args       []string
	Dir        string
	Env        []string
	Cols       uint32
	Rows       uint32
	ID         string
	Generation uint64
	OnClose    func()
}

type terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows uint32) error
	Wait(ctx context.Context) error
	Kill() error
	PID() int
}

type Session struct {
	terminal   terminal
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
	id         string
	generation uint64
	onClose    func()
	writeMu    sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func Start(ctx context.Context, config Config) (*Session, error) {
	if config.Path == "" {
		return nil, errors.New("PTY executable is empty")
	}
	if config.Cols == 0 {
		config.Cols = 80
	}
	if config.Rows == 0 {
		config.Rows = 24
	}
	if err := validateSize(config.Cols, config.Rows); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(config.Path, config.Args...)
	cmd.Dir = config.Dir
	cmd.Env = config.Env
	terminal, err := startTerminal(cmd, config.Cols, config.Rows)
	if err != nil {
		return nil, err
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &Session{
		terminal:   terminal,
		ctx:        sessionCtx,
		cancel:     cancel,
		id:         config.ID,
		generation: config.Generation,
		onClose:    config.OnClose,
	}
	s.stop = context.AfterFunc(sessionCtx, func() { _ = s.Close() })
	return s, nil
}

func (s *Session) Read(p []byte) (int, error) {
	return s.terminal.Read(p)
}

func (s *Session) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.terminal.Write(p)
}

func (s *Session) Stderr() io.Reader {
	return bytes.NewReader(nil)
}

func (s *Session) Resize(ctx context.Context, cols, rows uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSize(cols, rows); err != nil {
		return err
	}
	return s.terminal.Resize(cols, rows)
}

func (s *Session) Wait(ctx context.Context) error {
	waitCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	return s.terminal.Wait(waitCtx)
}

func (s *Session) CloseWrite() error {
	return base.ErrUnsupported
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Generation() uint64 {
	return s.generation
}

func (s *Session) PID() int {
	return s.terminal.PID()
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		s.cancel()
		killErr := s.terminal.Kill()
		closeErr := s.terminal.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.terminal.Wait(ctx)
		cancel()
		s.closeErr = errors.Join(killErr, closeErr)
		if s.onClose != nil {
			s.onClose()
		}
	})
	if errors.Is(s.closeErr, os.ErrProcessDone) {
		return nil
	}
	return s.closeErr
}

func validateSize(cols, rows uint32) error {
	if cols == 0 || rows == 0 || cols > maxDimension || rows > maxDimension {
		return fmt.Errorf("PTY dimensions must be between 1 and %d", maxDimension)
	}
	return nil
}

var _ base.Channel = (*Session)(nil)
