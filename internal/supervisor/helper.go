package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/shellenv"
)

const (
	HelperCommand             = "supervisor-helper"
	helperProbeTimeout        = 5 * time.Second
	defaultHelperSpawnTimeout = 30 * time.Second
	helperShutdownTimeout     = 15 * time.Second
)

type HelperConfig struct {
	StateDir       string
	Executable     string
	CommandTimeout time.Duration
	SpawnTimeout   time.Duration
	Bridge         bool
	Probe          bool
	// DisableStateDetection turns off per-session attention state. A helper
	// spawned by ConnectHelper receives it as --state-detection=off; the
	// helper's StateDetectionEnv overrides it either way.
	DisableStateDetection bool
	// Limits are passed to a helper this call spawns and applied by RunHelper.
	Limits Limits
	// NoSpawn attaches only: when no helper answers, ConnectHelper returns
	// ErrHelperNotRunning instead of starting one.
	NoSpawn bool
}

// StateDetectionEnv in the helper's environment overrides the
// --state-detection option: "off", "0", "false" or "no" disables
// per-session attention state detection and "on", "1", "true" or "yes"
// enables it.
const StateDetectionEnv = "NEXTERM_HELPER_STATE_DETECTION"

// stateDetectionSetting parses an on/off value; ok is false when value is
// not one.
func stateDetectionSetting(value string) (disabled, ok bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "false", "no":
		return true, true
	case "on", "1", "true", "yes":
		return false, true
	}
	return false, false
}

// ErrHelperExited reports a helper this process spawned that exited before it
// served its endpoint.
var ErrHelperExited = fmt.Errorf("%w: supervisor helper exited during startup", ErrUnavailable)

type Helper struct {
	endpoint string
	client   *Client
	spawned  bool
	process  *helperProcess
}

func (h *Helper) Endpoint() string {
	return h.endpoint
}

func (h *Helper) Client() *Client {
	return h.client
}

func (h *Helper) Spawned() bool {
	return h.spawned
}

// Exited is closed when a helper this call spawned exits. It is nil for a
// helper that was already running, whose exit this process cannot observe.
func (h *Helper) Exited() <-chan struct{} {
	if h.process == nil {
		return nil
	}
	return h.process.done
}

// PID is the process ID of a helper this call spawned, or zero.
func (h *Helper) PID() int {
	if h.process == nil {
		return 0
	}
	return h.process.pid
}

// JobBound reports a Windows helper that could not leave this process's job
// object and so may end when the job closes.
func (h *Helper) JobBound() bool {
	return h.process != nil && h.process.jobBound
}

func HelperEndpoint(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	return helperEndpoint(absolute)
}

func ConnectHelper(ctx context.Context, config HelperConfig) (*Helper, error) {
	if config.StateDir == "" {
		return nil, fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil {
		return nil, fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(stateDir); err != nil {
		return nil, fmt.Errorf("supervisor helper state directory: %w", err)
	}
	endpoint, err := helperEndpoint(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve supervisor helper endpoint: %w", err)
	}
	client := NewClient(endpoint, stateDir)
	err = probeHelper(ctx, client)
	if err == nil {
		return &Helper{endpoint: endpoint, client: client}, nil
	}
	if errors.Is(err, ErrProtocol) {
		return nil, fmt.Errorf("the supervisor helper at %s speaks an incompatible protocol; refusing to replace it: %w", endpoint, err)
	}
	if errors.Is(err, ErrStateMismatch) {
		return nil, fmt.Errorf("%w: the endpoint %s is already served by a helper owning a different state directory; refusing to connect or replace it: %v", ErrStateMismatch, endpoint, err)
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if config.NoSpawn {
		return nil, fmt.Errorf("%w: %s: %v", ErrHelperNotRunning, endpoint, err)
	}
	executable := config.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve supervisor helper executable: %w", err)
		}
	}
	spawnTimeout := config.SpawnTimeout
	if spawnTimeout <= 0 {
		spawnTimeout = defaultHelperSpawnTimeout
	}
	logPath := filepath.Join(stateDir, "helper.log")
	process, err := spawnDetached(executable, helperSpawnArgs(stateDir, config), logPath)
	if err != nil {
		return nil, fmt.Errorf("spawn supervisor helper: %w", err)
	}
	deadline := time.Now().Add(spawnTimeout)
	exited := process.done
	for {
		err = probeHelper(ctx, client)
		if err == nil {
			if !process.serves(ctx, endpoint) {
				// Another helper won the state lock and serves the endpoint.
				return &Helper{endpoint: endpoint, client: client}, nil
			}
			return &Helper{endpoint: endpoint, client: client, spawned: true, process: process}, nil
		}
		if errors.Is(err, ErrProtocol) {
			return nil, fmt.Errorf("the spawned supervisor helper at %s speaks an incompatible protocol: %w", endpoint, err)
		}
		if errors.Is(err, ErrStateMismatch) {
			return nil, fmt.Errorf("%w: the endpoint %s is owned by a helper for a different state directory: %v", ErrStateMismatch, endpoint, err)
		}
		if time.Now().After(deadline) {
			if exited == nil {
				return nil, fmt.Errorf("%w: %v (log: %s): %v", ErrHelperExited, process.exitErr(), logPath, err)
			}
			// A helper that never served may still hold the state lock; end
			// it so the next attempt can start.
			process.kill()
			return nil, fmt.Errorf("%w: supervisor helper did not become ready within %s (log: %s): %v", ErrUnavailable, spawnTimeout, logPath, err)
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-exited:
			// The spawned helper is gone; a concurrent helper that holds the
			// state lock may still be starting, so keep probing briefly.
			exited = nil
			if grace := time.Now().Add(helperExitGrace); grace.Before(deadline) {
				deadline = grace
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// helperExitGrace is how long ConnectHelper keeps probing after the helper it
// spawned exited, for a concurrently started helper to come up.
const helperExitGrace = 2 * time.Second

func probeHelper(ctx context.Context, client *Client) error {
	probeCtx, cancel := context.WithTimeout(ctx, helperProbeTimeout)
	defer cancel()
	_, err := client.List(probeCtx)
	return err
}

func RunHelper(ctx context.Context, config HelperConfig) error {
	if config.StateDir == "" {
		return fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	stateDir, err := filepath.Abs(config.StateDir)
	if err != nil {
		return fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	if err := ensurePrivateDir(stateDir); err != nil {
		return fmt.Errorf("supervisor helper state directory: %w", err)
	}
	unlock, err := lockHelperState(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	hlog := newHelperLog(stateDir)
	defer hlog.close()
	hlog.logStart(stateDir)
	// Resolve the login-shell environment now so the first terminal does not wait for it.
	shellenv.Default().Start()
	if err := os.WriteFile(filepath.Join(stateDir, "helper.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("write supervisor helper pid: %w", err)
	}
	defer func() { _ = os.Remove(filepath.Join(stateDir, "helper.pid")) }()
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: config.CommandTimeout, DisableStateDetection: config.DisableStateDetection, Limits: config.Limits})
	if err != nil {
		return err
	}
	supervisor.log = hlog
	endpoint, err := helperEndpoint(stateDir)
	if err != nil {
		_ = supervisor.Close()
		return fmt.Errorf("resolve supervisor helper endpoint: %w", err)
	}
	server, err := NewServer(supervisor, endpoint)
	if err != nil {
		_ = supervisor.Close()
		return err
	}
	hlog.event("ready", "endpoint", endpoint)
	<-awaitHelperStop(ctx)
	hlog.event("shutdown_begin")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), helperShutdownTimeout)
	defer cancel()
	err = errors.Join(server.Shutdown(shutdownCtx), supervisor.Close())
	hlog.event("shutdown_done", "error", err)
	return err
}

func RunHelperCLI(args []string) int {
	config, err := parseHelperArgs(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, HelperCommand+":", err)
		return 2
	}
	if config.Probe {
		fmt.Fprintln(os.Stdout, ProtocolVersion)
		return 0
	}
	if config.Bridge {
		stateDir, err := filepath.Abs(config.StateDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, HelperCommand+":", err)
			return 2
		}
		config.StateDir = stateDir
		if err := runBridge(context.Background(), config); err != nil {
			fmt.Fprintln(os.Stderr, HelperCommand+":", err)
			return 1
		}
		return 0
	}
	if err := RunHelper(context.Background(), config); err != nil {
		fmt.Fprintln(os.Stderr, HelperCommand+":", err)
		return 1
	}
	return 0
}

// helperSpawnArgs are the arguments ConnectHelper starts a helper with.
func helperSpawnArgs(stateDir string, config HelperConfig) []string {
	args := []string{HelperCommand, "--state-dir", stateDir}
	if config.DisableStateDetection {
		args = append(args, "--state-detection=off")
	}
	return append(args, config.Limits.args()...)
}

func parseHelperArgs(args []string, getenv func(string) string) (HelperConfig, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	var config HelperConfig
	var dataDir string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !strings.HasPrefix(argument, "--") {
			return HelperConfig{}, fmt.Errorf("unexpected argument %q", argument)
		}
		name, value, hasValue := strings.Cut(argument, "=")
		if name == "--bridge" || name == "--probe" {
			if hasValue {
				return HelperConfig{}, fmt.Errorf("option %s takes no value", name)
			}
			if name == "--bridge" {
				config.Bridge = true
			} else {
				config.Probe = true
			}
			continue
		}
		if name != "--state-dir" && name != "--data-dir" && name != "--state-detection" && !limitOption(name) {
			return HelperConfig{}, fmt.Errorf("unknown option %s", name)
		}
		if !hasValue {
			index++
			if index >= len(args) {
				return HelperConfig{}, fmt.Errorf("option %s requires a value", name)
			}
			value = args[index]
		}
		switch name {
		case "--state-dir":
			config.StateDir = value
		case "--data-dir":
			dataDir = value
		case "--state-detection":
			disabled, ok := stateDetectionSetting(value)
			if !ok {
				return HelperConfig{}, fmt.Errorf("option %s takes on or off, not %q", name, value)
			}
			config.DisableStateDetection = disabled
		default:
			if err := config.Limits.set(name, value); err != nil {
				return HelperConfig{}, err
			}
		}
	}
	if disabled, ok := stateDetectionSetting(getenv(StateDetectionEnv)); ok {
		config.DisableStateDetection = disabled
	}
	if config.StateDir == "" && !config.Probe {
		if dataDir == "" {
			dataDir = getenv("NEXTERM_DATA_DIR")
		}
		if dataDir == "" {
			return HelperConfig{}, fmt.Errorf("a state directory or data directory is required")
		}
		config.StateDir = filepath.Join(dataDir, "durable", "supervisor")
	}
	return config, nil
}
