package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var nextGeneration atomic.Uint64

type Config struct {
	Shell string
	CWD   string
	Env   map[string]string
}

type Transport struct {
	shell         string
	loginShell    bool
	cwd           string
	env           map[string]string
	generation    uint64
	filesystem    *fslocal.FileSystem
	done          chan struct{}
	closeMu       sync.Mutex
	closeComplete bool
	doneClosed    bool
	closeErr      error
	nextID        atomic.Uint64
	mu            sync.Mutex
	channels      map[string]io.Closer
}

func NewWithConfig(config Config) *Transport {
	shell := strings.TrimSpace(config.Shell)
	loginShell := true
	if shell == "" {
		shell, loginShell = defaultShell()
	}
	cwd := strings.TrimSpace(config.CWD)
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	env := make(map[string]string, len(config.Env))
	for key, value := range config.Env {
		env[key] = value
	}
	return &Transport{
		shell:      shell,
		loginShell: loginShell,
		cwd:        cwd,
		env:        env,
		generation: nextGeneration.Add(1),
		filesystem: fslocal.New(),
		done:       make(chan struct{}),
		channels:   make(map[string]io.Closer),
	}
}

func (t *Transport) Kind() string {
	return "local"
}

func (t *Transport) Shell() string {
	executable := t.shell
	if index := strings.LastIndexAny(executable, "/\\"); index >= 0 {
		executable = executable[index+1:]
	}
	switch strings.ToLower(executable) {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return "powershell"
	case "cmd", "cmd.exe":
		return "cmd"
	}
	switch executable {
	case "sh", "bash", "zsh", "dash", "ksh":
		return "posix"
	default:
		return "unknown"
	}
}

func (t *Transport) Generation() uint64 {
	return t.generation
}

func (t *Transport) IsAlive() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

func (t *Transport) Ping(ctx context.Context) (time.Duration, error) {
	started := time.Now()
	return time.Since(started), t.check(ctx, 0)
}

func (t *Transport) FileSystem(ctx context.Context) (base.FileSystem, error) {
	if err := t.check(ctx, 0); err != nil {
		return nil, err
	}
	return t.filesystem, nil
}

func (t *Transport) Close() error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closeComplete {
		return t.closeErr
	}
	if !t.doneClosed {
		close(t.done)
		t.doneClosed = true
	}
	t.mu.Lock()
	channels := make([]io.Closer, 0, len(t.channels))
	for _, channel := range t.channels {
		channels = append(channels, channel)
	}
	t.mu.Unlock()
	errs := make([]error, 0, len(channels))
	for _, channel := range channels {
		if err := channel.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	t.closeErr = errors.Join(errs...)
	t.closeComplete = t.closeErr == nil
	return t.closeErr
}

func (t *Transport) check(ctx context.Context, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if generation != 0 && generation != t.generation {
		return base.ErrStaleGeneration
	}
	if !t.IsAlive() {
		return base.ErrClosed
	}
	return nil
}

func (t *Transport) channelID(kind string) string {
	return fmt.Sprintf("local-%s-%d-%d", kind, t.generation, t.nextID.Add(1))
}

func (t *Transport) track(ctx context.Context, channel base.Channel) (base.Channel, error) {
	tracked := &trackedChannel{Channel: channel}
	tracked.onClose = func() {
		t.mu.Lock()
		delete(t.channels, channel.ID())
		t.mu.Unlock()
	}
	t.mu.Lock()
	if !t.IsAlive() {
		t.mu.Unlock()
		_ = tracked.Close()
		return nil, base.ErrClosed
	}
	t.channels[channel.ID()] = tracked
	t.mu.Unlock()
	tracked.setStop(context.AfterFunc(ctx, func() { _ = tracked.Close() }))
	if err := ctx.Err(); err != nil {
		_ = tracked.Close()
		return nil, err
	}
	return tracked, nil
}

type trackedChannel struct {
	base.Channel
	closeMu       sync.Mutex
	closeComplete bool
	closeErr      error
	onClose       func()
	mu            sync.Mutex
	stop          func() bool
	closed        bool
}

func (c *trackedChannel) setStop(stop func() bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		stop()
		return
	}
	c.stop = stop
	c.mu.Unlock()
}

func (c *trackedChannel) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closeComplete {
		return c.closeErr
	}
	c.mu.Lock()
	c.closed = true
	stop := c.stop
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	c.closeErr = c.Channel.Close()
	if c.closeErr == nil {
		c.closeComplete = true
		if c.onClose != nil {
			c.onClose()
		}
	}
	return c.closeErr
}

var (
	_ base.Transport           = (*Transport)(nil)
	_ base.PTYTransport        = (*Transport)(nil)
	_ base.ExecStreamTransport = (*Transport)(nil)
	_ base.FileTransport       = (*Transport)(nil)
)
