package shellenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/usershell"
)

// Resolution methods reported in Status.Method.
const (
	MethodInteractive = "interactive-login" // shell -l -i -c
	MethodLogin       = "login"             // shell -l -c, used when the interactive attempt failed
	MethodInherited   = "inherited"         // process environment trusted as is (Linux fast path)
	MethodProcess     = "process"           // every attempt failed or resolution is not applicable
)

// StatusFileName is written next to the shell file so `nexterm-agent status` can show the last result of
// the long-lived process that actually opens terminals.
const StatusFileName = "shellenv-status.json"

// Status describes the last resolution.
type Status struct {
	Shell       string    `json:"shell"`
	ShellSource Source    `json:"shellSource"`
	ShellNote   string    `json:"shellNote,omitempty"`
	Method      string    `json:"method"`
	PathSource  string    `json:"pathSource"`
	ResolvedAt  time.Time `json:"resolvedAt"`
	DurationMS  int64     `json:"durationMs"`
	Error       string    `json:"error,omitempty"`
	Pid         int       `json:"pid"`
}

// Options configures a Resolver; zero values select production behavior.
type Options struct {
	Explicit func() string // explicit shell setting
	Account  func() string // account login shell
	Getenv   func(string) string
	Environ  func() []string
	GOOS     string
	Timeout  time.Duration // per attempt
	TTL      time.Duration
	Now      func() time.Time
	Disabled bool // never spawn a shell; the process environment is used
	OnResult func(Status)
}

// Resolver resolves and caches the user's shell environment.
type Resolver struct {
	opts Options

	mu       sync.Mutex
	env      []string
	have     bool
	at       time.Time
	status   Status
	inflight chan struct{}

	accountMu    sync.Mutex
	accountShell string
	accountAt    time.Time
}

// New returns a Resolver with defaults filled in.
func New(opts Options) *Resolver {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.TTL <= 0 {
		opts.TTL = 10 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &Resolver{opts: opts}
	if opts.Explicit == nil {
		r.opts.Explicit = func() string { return ExplicitFromEnv(r.opts.Getenv) }
	}
	if opts.Account == nil {
		r.opts.Account = usershell.Lookup
	}
	return r
}

var (
	defaultMu       sync.Mutex
	defaultResolver *Resolver
)

// Default returns the process-wide resolver.
func Default() *Resolver {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultResolver == nil {
		defaultResolver = New(Options{OnResult: PersistStatus})
	}
	return defaultResolver
}

// SetDefault replaces the process-wide resolver; tests use it to avoid spawning login shells.
func SetDefault(r *Resolver) {
	defaultMu.Lock()
	defaultResolver = r
	defaultMu.Unlock()
}

// PersistStatus writes the status next to the shell file when the agent configured one.
func PersistStatus(s Status) {
	file := os.Getenv(ShellFileEnv)
	if file == "" {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(file), StatusFileName), data, 0o600)
}

// LoadStatus reads a status file written by PersistStatus.
func LoadStatus(dataDir string) (Status, bool) {
	data, err := os.ReadFile(filepath.Join(dataDir, StatusFileName))
	if err != nil {
		return Status{}, false
	}
	var s Status
	if json.Unmarshal(data, &s) != nil {
		return Status{}, false
	}
	return s, true
}

// accountShellCached avoids spawning dscl or getent for every session.
func (r *Resolver) accountShellCached() string {
	r.accountMu.Lock()
	defer r.accountMu.Unlock()
	if r.accountAt.IsZero() || r.opts.Now().Sub(r.accountAt) > r.opts.TTL {
		r.accountShell = r.opts.Account()
		r.accountAt = r.opts.Now()
	}
	return r.accountShell
}

// Shell applies the shell precedence. request is a shell the caller asked for explicitly for one session.
func (r *Resolver) Shell(request string) Choice {
	return Choose(Inputs{
		Request:  request,
		Explicit: r.opts.Explicit(),
		Account:  r.accountShellCached,
		Env:      r.opts.Getenv("SHELL"),
	})
}

// Start begins resolving in the background so the first terminal does not pay for it.
func (r *Resolver) Start() { r.begin() }

// Refresh invalidates the cache and resolves again in the background; the previous result keeps serving
// until the new one lands.
func (r *Resolver) Refresh() {
	r.mu.Lock()
	r.at = time.Time{}
	r.accountMu.Lock()
	r.accountAt = time.Time{}
	r.accountMu.Unlock()
	r.mu.Unlock()
	r.begin()
}

// Status reports the last resolution; the zero value means nothing resolved yet.
func (r *Resolver) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Env returns the process environment overlaid with the user's shell environment. When nothing is cached it
// waits at most wait for the resolution; after that, or on any failure, it returns the best known
// environment (the previous result, else the process environment). It never fails.
func (r *Resolver) Env(wait time.Duration) []string {
	if r.opts.Disabled || r.opts.GOOS == "windows" {
		return r.opts.Environ()
	}
	r.mu.Lock()
	fresh := r.have && r.opts.Now().Sub(r.at) < r.opts.TTL
	if fresh {
		env := append([]string(nil), r.env...)
		r.mu.Unlock()
		return env
	}
	r.mu.Unlock()
	done := r.begin()
	r.mu.Lock()
	stale := r.have
	r.mu.Unlock()
	if !stale && done != nil {
		timer := time.NewTimer(wait)
		select {
		case <-done:
		case <-timer.C:
		}
		timer.Stop()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.have {
		return append([]string(nil), r.env...)
	}
	return r.opts.Environ()
}

// begin starts a resolution unless one is running and returns its completion channel.
func (r *Resolver) begin() <-chan struct{} {
	if r.opts.Disabled || r.opts.GOOS == "windows" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight != nil {
		return r.inflight
	}
	done := make(chan struct{})
	r.inflight = done
	go func() {
		env, status := r.resolve()
		if r.opts.OnResult != nil {
			r.opts.OnResult(status)
		}
		r.mu.Lock()
		r.env, r.have, r.at, r.status = env, true, r.opts.Now(), status
		r.inflight = nil
		r.mu.Unlock()
		close(done)
	}()
	return done
}

// resolve always returns a usable environment; failures are recorded in the status and logged at debug level.
func (r *Resolver) resolve() ([]string, Status) {
	started := r.opts.Now()
	base := r.opts.Environ()
	choice := r.Shell("")
	status := Status{
		Shell: choice.Path, ShellSource: choice.Source, ShellNote: choice.Note,
		Method: MethodProcess, PathSource: "process environment", Pid: os.Getpid(), ResolvedAt: started,
	}
	finish := func() ([]string, Status) {
		status.DurationMS = r.opts.Now().Sub(started).Milliseconds()
		if status.Error != "" {
			slog.Debug("shell environment resolution fell back", "method", status.Method, "shell", status.Shell, "reason", status.Error)
		}
		return base, status
	}
	if choice.Source == SourceFallback {
		base = overlay(base, nil, EnvValue(base, "PATH"), choice.Path)
		status.Error = "no login shell available; using /bin/sh and the process environment"
		return finish()
	}
	if r.opts.GOOS == "linux" && choice.Source == SourceEnv && !pathMinimal(EnvValue(base, "PATH")) {
		status.Method, status.PathSource = MethodInherited, "inherited from the process"
		return finish()
	}
	var failures []string
	attempts := []struct {
		method string
		args   []string
	}{
		{MethodInteractive, []string{"-l", "-i", "-c"}},
		{MethodLogin, []string{"-l", "-c"}},
	}
	for _, attempt := range attempts {
		resolved, path, err := r.attempt(choice.Path, attempt.args, base)
		if err != nil {
			failures = append(failures, attempt.method+": "+err.Error())
			continue
		}
		status.Method, status.PathSource = attempt.method, "login shell ("+attempt.method+")"
		if len(failures) > 0 {
			status.Error = strings.Join(failures, "; ")
		}
		base = overlay(base, resolved, path, choice.Path)
		return finish()
	}
	status.Error = strings.Join(failures, "; ")
	base = overlay(base, nil, EnvValue(base, "PATH"), choice.Path)
	return finish()
}

func (r *Resolver) attempt(shell string, flags, base []string) (map[string]string, string, error) {
	marker, err := newMarker()
	if err != nil {
		return nil, "", err
	}
	env := append([]string(nil), base...)
	env = setEnv(env, "TERM", "dumb")
	env = setEnv(env, ResolvingEnv, "1")
	env = setEnv(env, "SHELL", shell)
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.Timeout)
	defer cancel()
	out, runErr := runShell(ctx, shell, append(append([]string(nil), flags...), probeScript(marker)), env)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, "", fmt.Errorf("timed out after %s", r.opts.Timeout)
	}
	resolved, parseErr := parseMarked(out, marker)
	if parseErr != nil {
		if runErr != nil {
			return nil, "", fmt.Errorf("%v (%v)", parseErr, runErr)
		}
		return nil, "", parseErr
	}
	path, err := validatePATH(resolved["PATH"])
	if err != nil {
		return nil, "", err
	}
	return resolved, path, nil
}

func setEnv(env []string, key, value string) []string {
	out := env[:0]
	for _, entry := range env {
		if k, _, _ := strings.Cut(entry, "="); k != key {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}
