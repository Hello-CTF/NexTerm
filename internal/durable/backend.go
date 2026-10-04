package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

const (
	DefaultNamespace = "nexterm-durable"
	maxDimension     = 1024
	ownerOption      = "@nexterm_durable_id"
	namespaceOption  = "@nexterm_durable_namespace"
	paneOption       = "@nexterm_durable_pane"
	windowOption     = "@nexterm_durable_window"
)

type Config struct {
	Binary         string
	SocketPath     string
	StateDir       string
	Namespace      string
	CommandTimeout time.Duration
}

type CreateOptions struct {
	ID      string
	Command []string
	Dir     string
	Env     []string
	Cols    uint32
	Rows    uint32
}

type Info struct {
	ID        string
	SessionID string
	PaneID    string
	PID       int
	CreatedAt time.Time
	Dead      bool
	ExitCode  *int
	Signal    string
	Cols      uint32
	Rows      uint32
}

type Backend struct {
	binary         string
	socketPath     string
	stateDir       string
	namespace      string
	commandTimeout time.Duration
	pollInterval   time.Duration
	statusInterval time.Duration
	runner         runCommand
	cleanupMu      sync.Mutex
	killedMu       sync.Mutex
	killed         map[string]struct{}
}

func New(config Config) (*Backend, error) {
	if config.Binary == "" {
		config.Binary = "tmux"
	}
	if config.Namespace == "" {
		config.Namespace = DefaultNamespace
	}
	if config.CommandTimeout == 0 {
		config.CommandTimeout = 5 * time.Second
	}
	if config.SocketPath == "" || config.StateDir == "" {
		return nil, fmt.Errorf("%w: SocketPath and StateDir are required", ErrInvalidInput)
	}
	if config.CommandTimeout < 0 {
		return nil, fmt.Errorf("%w: negative command timeout", ErrInvalidInput)
	}
	if err := validateNamespace(config.Namespace); err != nil {
		return nil, err
	}
	binary, err := exec.LookPath(config.Binary)
	if err != nil {
		return nil, fmt.Errorf("%w: tmux executable %q: %v", ErrUnavailable, config.Binary, err)
	}
	socketPath, err := filepath.Abs(config.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: socket path: %v", ErrInvalidInput, err)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil {
		return nil, fmt.Errorf("%w: state directory: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(filepath.Dir(socketPath)); err != nil {
		return nil, fmt.Errorf("tmux socket directory: %w", err)
	}
	if err := ensurePrivateDir(stateDir); err != nil {
		return nil, fmt.Errorf("durable state directory: %w", err)
	}
	backend := &Backend{
		binary:         binary,
		socketPath:     socketPath,
		stateDir:       stateDir,
		namespace:      config.Namespace,
		commandTimeout: config.CommandTimeout,
		pollInterval:   20 * time.Millisecond,
		statusInterval: 250 * time.Millisecond,
		killed:         make(map[string]struct{}),
	}
	backend.runner = backend.execute
	return backend, nil
}

func (b *Backend) Create(ctx context.Context, options CreateOptions) (*Session, error) {
	id := options.ID
	if id == "" {
		id = ids.New()
	}
	if !ids.Valid(id) {
		return nil, fmt.Errorf("%w: invalid durable terminal ID", ErrInvalidInput)
	}
	if options.Cols == 0 {
		options.Cols = 80
	}
	if options.Rows == 0 {
		options.Rows = 24
	}
	if err := validateSize(options.Cols, options.Rows); err != nil {
		return nil, err
	}
	launch, err := b.launchCommand(id, options)
	if err != nil {
		return nil, err
	}
	if _, err := b.resolve(ctx, id); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := os.Mkdir(b.sessionDir(id), 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: artifacts for %s", ErrAlreadyExists, id)
		}
		return nil, fmt.Errorf("create durable artifact directory: %w", err)
	}
	recording, err := os.OpenFile(b.recordingPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = b.removeArtifacts(id)
		return nil, fmt.Errorf("create durable recording: %w", err)
	}
	if err := recording.Close(); err != nil {
		_ = b.removeArtifacts(id)
		return nil, fmt.Errorf("close durable recording: %w", err)
	}
	name := b.sessionName(id)
	args := []string{
		"new-session", "-d", "-s", name,
		"-x", strconv.FormatUint(uint64(options.Cols), 10),
		"-y", strconv.FormatUint(uint64(options.Rows), 10),
	}
	if options.Dir != "" {
		args = append(args, "-c", options.Dir)
	}
	for _, entry := range options.Env {
		args = append(args, "-e", entry)
	}
	args = append(args, launch,
		";", "set-option", "-t", name, ownerOption, id,
		";", "set-option", "-t", name, namespaceOption, b.namespace,
		";", "set-option", "-F", "-t", name, paneOption, "#{pane_id}",
		";", "set-option", "-F", "-t", name, windowOption, "#{window_id}",
		";", "set-option", "-t", name, "destroy-unattached", "off",
		";", "set-option", "-w", "-t", name, "remain-on-exit", "on",
		";", "set-option", "-w", "-t", name, "window-size", "manual",
		";", "pipe-pane", "-O", "-t", name, b.pipeCommand(id),
	)
	if _, err := b.run(ctx, args...); err != nil {
		return nil, b.abortCreate(id, err)
	}
	current, err := b.resolve(ctx, id)
	if err != nil {
		return nil, b.abortCreate(id, err)
	}
	if current.info.Dead || !current.recordingLive {
		return nil, b.abortCreate(id, fmt.Errorf("%w: tmux output recording did not start", ErrUnavailable))
	}
	file, err := b.openRecording(id)
	if err != nil {
		return nil, b.abortCreate(id, err)
	}
	gate, err := os.OpenFile(b.gatePath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = file.Close()
		return nil, b.abortCreate(id, fmt.Errorf("release durable shell: %w", err))
	}
	if err := gate.Close(); err != nil {
		_ = file.Close()
		return nil, b.abortCreate(id, fmt.Errorf("release durable shell: %w", err))
	}
	if err := b.waitForLaunch(ctx, id); err != nil {
		_ = file.Close()
		return nil, b.abortCreate(id, err)
	}
	return newSession(b, current, file), nil
}

func (b *Backend) Attach(ctx context.Context, id string) (*Session, error) {
	current, err := b.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	if !current.info.Dead && !current.recordingLive {
		closing, err := b.recorderClosing(id)
		if err != nil {
			return nil, err
		}
		if !closing {
			return nil, fmt.Errorf("%w: tmux output recording is not running for %s", ErrUnavailable, id)
		}
	}
	file, err := b.openRecording(id)
	if err != nil {
		return nil, err
	}
	return newSession(b, current, file), nil
}

func (b *Backend) Kill(ctx context.Context, id string) error {
	return b.kill(ctx, id, nil)
}

func (b *Backend) kill(ctx context.Context, id string, expected *record) error {
	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()
	current, err := b.resolve(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return b.finishMissingKill(ctx, id)
	}
	if err != nil {
		return err
	}
	if expected != nil && (!sameIdentity(expected.info, current.info) || expected.windowID != current.windowID) {
		return fmt.Errorf("%w: %s", ErrIdentity, id)
	}
	if _, err := b.run(ctx, "kill-session", "-t", current.info.SessionID); err != nil {
		if _, resolveErr := b.resolve(ctx, id); errors.Is(resolveErr, ErrNotFound) {
			return b.finishMissingKill(ctx, id)
		}
		return err
	}
	return b.finishKill(ctx, id)
}

func (b *Backend) abortCreate(id string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), b.commandTimeout)
	defer cancel()
	current, err := b.resolve(ctx, id)
	if err == nil {
		if _, killErr := b.run(ctx, "kill-session", "-t", current.info.SessionID); killErr != nil {
			return errors.Join(cause, fmt.Errorf("clean up incomplete durable terminal: %w", killErr))
		}
	} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotOwned) {
		return errors.Join(cause, fmt.Errorf("reconcile incomplete durable terminal: %w", err))
	}
	if cleanupErr := b.removeArtifacts(id); cleanupErr != nil {
		return errors.Join(cause, cleanupErr)
	}
	return cause
}

func (b *Backend) launchCommand(id string, options CreateOptions) (string, error) {
	command := options.Command
	if len(command) == 0 {
		shell := environmentValue(options.Env, "SHELL")
		if shell == "" {
			shell = os.Getenv("SHELL")
		}
		if shell == "" {
			shell = "/bin/sh"
		}
		command = []string{shell, "-l"}
	}
	if command[0] == "" {
		return "", fmt.Errorf("%w: empty shell executable", ErrInvalidInput)
	}
	for _, argument := range command {
		if strings.ContainsRune(argument, 0) {
			return "", fmt.Errorf("%w: NUL in shell command", ErrInvalidInput)
		}
	}
	for _, entry := range options.Env {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" || strings.ContainsRune(entry, 0) {
			return "", fmt.Errorf("%w: environment entries must be KEY=VALUE", ErrInvalidInput)
		}
	}
	if strings.ContainsRune(options.Dir, 0) {
		return "", fmt.Errorf("%w: NUL in working directory", ErrInvalidInput)
	}
	quoted := make([]string, len(command))
	for index, argument := range command {
		quoted[index] = shellQuote(argument)
	}
	launch := strings.Join(quoted, " ")
	return "exec /bin/sh -c " + shellQuote(b.supervisorScript(id, launch)), nil
}

func (b *Backend) supervisorScript(id, launch string) string {
	gate := shellQuote(b.gatePath(id))
	closeRecording := shellQuote(b.binary) + " -f " + shellQuote(os.DevNull) + " -S " + shellQuote(b.socketPath) + " pipe-pane -t \"$TMUX_PANE\""
	script := []string{
		"while [ ! -f " + gate + " ]; do sleep 0.05; done; rm -f " + gate,
		"_nexterm_durable_status=",
		"_nexterm_durable_finish() { umask 077; : > " + shellQuote(b.recorderClosingTempPath(id)) + "; mv " + shellQuote(b.recorderClosingTempPath(id)) + " " + shellQuote(b.recorderClosingPath(id)) + "; " + closeRecording + "; exit \"$1\"; }",
		"_nexterm_durable_on_signal() { _nexterm_durable_status=$?; trap ':' HUP INT QUIT TERM; kill -s \"$1\" 0 2>/dev/null; wait; if [ -z \"$_nexterm_durable_status\" ] || [ \"$_nexterm_durable_status\" -eq 0 ]; then _nexterm_durable_status=$((128 + $2)); fi; _nexterm_durable_finish \"$_nexterm_durable_status\"; }",
		"trap '_nexterm_durable_on_signal HUP 1' HUP",
		"trap '_nexterm_durable_on_signal INT 2' INT",
		"trap '_nexterm_durable_on_signal QUIT 3' QUIT",
		"trap '_nexterm_durable_on_signal TERM 15' TERM",
		": > " + shellQuote(b.launchStartedPath(id)),
		launch,
		"_nexterm_durable_status=$?",
		"_nexterm_durable_finish \"$_nexterm_durable_status\"",
	}
	return strings.Join(script, "; ")
}

func (b *Backend) pipeCommand(id string) string {
	recording := strings.ReplaceAll(b.recordingPath(id), "#", "##")
	doneTemp := strings.ReplaceAll(b.recorderDoneTempPath(id), "#", "##")
	done := strings.ReplaceAll(b.recorderDonePath(id), "#", "##")
	return "umask 077; cat >> " + shellQuote(recording) +
		"; _nexterm_durable_recorder=$?; printf \"$_nexterm_durable_recorder\\n\" > " + shellQuote(doneTemp) +
		"; mv " + shellQuote(doneTemp) + " " + shellQuote(done)
}

func (b *Backend) openRecording(id string) (*os.File, error) {
	path := b.recordingPath(id)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open durable recording: %v", ErrUnavailable, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: durable recording is not a private regular file", ErrUnavailable)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open durable recording: %v", ErrUnavailable, err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: durable recording changed while opening", ErrUnavailable)
	}
	return file, nil
}

func (b *Backend) waitForLaunch(ctx context.Context, id string) error {
	launchCtx, cancel := context.WithTimeout(ctx, b.commandTimeout)
	defer cancel()
	for {
		_, err := os.Stat(b.launchStartedPath(id))
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect supervisor launch: %v", ErrUnavailable, err)
		}
		select {
		case <-launchCtx.Done():
			return fmt.Errorf("%w: supervisor did not launch: %v", ErrUnavailable, launchCtx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (b *Backend) removeArtifacts(id string) error {
	original := b.sessionDir(id)
	tombstone := b.tombstonePath(id)
	if _, err := os.Lstat(tombstone); err == nil {
		if err := os.RemoveAll(tombstone); err != nil {
			return fmt.Errorf("remove prior durable tombstone for %s: %w", id, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect durable tombstone for %s: %w", id, err)
	}
	if err := os.Rename(original, tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("quarantine durable artifacts for %s: %w", id, err)
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("remove durable tombstone for %s: %w", id, err)
	}
	for _, path := range []string{original, tombstone} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("durable artifact path remains: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("verify durable artifact cleanup for %s: %w", id, err)
		}
	}
	return nil
}

func (b *Backend) sessionName(id string) string { return b.namespace + "-" + id }
func (b *Backend) sessionDir(id string) string  { return filepath.Join(b.stateDir, id) }
func (b *Backend) tombstonePath(id string) string {
	return filepath.Join(b.stateDir, id+".delete")
}
func (b *Backend) recordingPath(id string) string {
	return filepath.Join(b.sessionDir(id), "output.raw")
}
func (b *Backend) gatePath(id string) string {
	return filepath.Join(b.sessionDir(id), "launch.ready")
}
func (b *Backend) launchStartedPath(id string) string {
	return filepath.Join(b.sessionDir(id), "supervisor.ready")
}
func (b *Backend) recorderDonePath(id string) string {
	return filepath.Join(b.sessionDir(id), "recorder.done")
}
func (b *Backend) recorderDoneTempPath(id string) string {
	return filepath.Join(b.sessionDir(id), "recorder.done.tmp")
}
func (b *Backend) recorderClosingPath(id string) string {
	return filepath.Join(b.sessionDir(id), "recorder.closing")
}
func (b *Backend) recorderClosingTempPath(id string) string {
	return filepath.Join(b.sessionDir(id), "recorder.closing.tmp")
}
func (b *Backend) versionsPath(id string) string {
	return filepath.Join(b.sessionDir(id), "versions")
}
func (b *Backend) versionsTempPath(id string) string {
	return filepath.Join(b.sessionDir(id), "versions.tmp")
}

func environmentValue(environment []string, name string) string {
	value := ""
	for _, entry := range environment {
		if key, current, found := strings.Cut(entry, "="); found && key == name {
			value = current
		}
	}
	return value
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func validateNamespace(namespace string) error {
	if namespace == "" || len(namespace) > 48 {
		return fmt.Errorf("%w: namespace length must be 1 to 48", ErrInvalidInput)
	}
	for index := 0; index < len(namespace); index++ {
		char := namespace[index]
		alphaNumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if !alphaNumeric && (index == 0 || char != '-' && char != '_') {
			return fmt.Errorf("%w: namespace contains invalid character %q", ErrInvalidInput, char)
		}
	}
	return nil
}

func validateSize(cols, rows uint32) error {
	if cols == 0 || rows == 0 || cols > maxDimension || rows > maxDimension {
		return fmt.Errorf("%w: dimensions must be between 1 and %d", ErrInvalidInput, maxDimension)
	}
	return nil
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a real directory", ErrInvalidInput, path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s must not be accessible by group or other users", ErrInvalidInput, path)
	}
	return nil
}
