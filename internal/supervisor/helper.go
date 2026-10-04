package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
}

type Helper struct {
	endpoint string
	client   *Client
	spawned  bool
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

func HelperEndpoint(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("%w: helper state directory is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("%w: helper state directory: %v", ErrInvalidInput, err)
	}
	return helperEndpoint(absolute), nil
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
	endpoint := helperEndpoint(stateDir)
	client := NewClient(endpoint)
	err = probeHelper(ctx, client)
	if err == nil {
		return &Helper{endpoint: endpoint, client: client}, nil
	}
	if errors.Is(err, ErrProtocol) {
		return nil, fmt.Errorf("%w: the supervisor helper at %s speaks an incompatible protocol; refusing to replace it: %v", ErrProtocol, endpoint, err)
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
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
	if err := spawnDetached(executable, []string{HelperCommand, "--state-dir", stateDir}, logPath); err != nil {
		return nil, fmt.Errorf("spawn supervisor helper: %w", err)
	}
	deadline := time.Now().Add(spawnTimeout)
	for {
		err = probeHelper(ctx, client)
		if err == nil {
			return &Helper{endpoint: endpoint, client: client, spawned: true}, nil
		}
		if errors.Is(err, ErrProtocol) {
			return nil, fmt.Errorf("%w: the spawned supervisor helper at %s speaks an incompatible protocol: %v", ErrProtocol, endpoint, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: supervisor helper did not become ready within %s (log: %s): %v", ErrUnavailable, spawnTimeout, logPath, err)
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

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
	supervisor, err := New(Config{StateDir: stateDir, CommandTimeout: config.CommandTimeout})
	if err != nil {
		return err
	}
	server, err := NewServer(supervisor, helperEndpoint(stateDir))
	if err != nil {
		_ = supervisor.Close()
		return err
	}
	<-awaitHelperStop(ctx)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), helperShutdownTimeout)
	defer cancel()
	return errors.Join(server.Shutdown(shutdownCtx), supervisor.Close())
}

func RunHelperCLI(args []string) int {
	config, err := parseHelperArgs(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, HelperCommand+":", err)
		return 2
	}
	if err := RunHelper(context.Background(), config); err != nil {
		fmt.Fprintln(os.Stderr, HelperCommand+":", err)
		return 1
	}
	return 0
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
		if name != "--state-dir" && name != "--data-dir" {
			return HelperConfig{}, fmt.Errorf("unknown option %s", name)
		}
		if !hasValue {
			index++
			if index >= len(args) {
				return HelperConfig{}, fmt.Errorf("option %s requires a value", name)
			}
			value = args[index]
		}
		if name == "--state-dir" {
			config.StateDir = value
		} else {
			dataDir = value
		}
	}
	if config.StateDir == "" {
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
