package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
)

type ServiceOption func(*Service)

func WithConfig(config Config) ServiceOption {
	return func(service *Service) {
		service.config = config.withDefaults()
	}
}

func WithAuditor(auditor Auditor) ServiceOption {
	return func(service *Service) {
		service.auditor = auditor
	}
}

func WithRegistryAuth(provider RegistryAuthProvider) ServiceOption {
	return func(service *Service) {
		service.registryAuth = provider
	}
}

type Service struct {
	provider     BackendProvider
	registryAuth RegistryAuthProvider
	auditor      Auditor
	config       Config
	streams      *streamRegistry
}

func NewService(provider BackendProvider, options ...ServiceOption) *Service {
	service := &Service{provider: provider, config: (Config{}).withDefaults()}
	for _, option := range options {
		option(service)
	}
	service.streams = newStreamRegistry()
	return service
}

func (s *Service) PS(ctx context.Context, sessionID string) ([]ContainerSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	return readWithFallback(ctx, s.provider, sessionID, func(backend Backend) ([]ContainerSummary, error) {
		if lister, ok := backend.(ContainerDTOLister); ok {
			return lister.ListContainerDTOs(ctx, true)
		}
		items, err := backend.ListContainers(ctx, true)
		if err != nil {
			return nil, err
		}
		return containerDTOs(items), nil
	})
}

func (s *Service) Overview(ctx context.Context, sessionID string) (Overview, error) {
	containers, err := s.PS(ctx, sessionID)
	if err != nil {
		return Overview{}, err
	}
	images, err := s.Images(ctx, sessionID)
	if err != nil {
		return Overview{}, err
	}
	host := HostStats{ContainersTotal: uint64(len(containers)), Images: uint64(len(images))}
	for _, item := range containers {
		if item.State == "running" {
			host.ContainersRunning++
		}
	}
	return Overview{Containers: containers, HostStats: host}, nil
}

func (s *Service) Inspect(ctx context.Context, sessionID, containerID string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	return readWithFallback(ctx, s.provider, sessionID, func(backend Backend) (json.RawMessage, error) {
		return backend.InspectContainer(ctx, containerID)
	})
}

func (s *Service) Images(ctx context.Context, sessionID string) ([]ImageSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	return readWithFallback(ctx, s.provider, sessionID, func(backend Backend) ([]ImageSummary, error) {
		if lister, ok := backend.(ImageDTOLister); ok {
			return lister.ListImageDTOs(ctx)
		}
		items, err := backend.ListImages(ctx)
		if err != nil {
			return nil, err
		}
		return imageDTOs(items, s.config.Now()), nil
	})
}

func (s *Service) Stats(ctx context.Context, sessionID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.StatsTimeout)
	defer cancel()
	var sdkErr error
	if backend, err := s.provider.SDK(ctx, sessionID); err == nil {
		output, operationErr := s.statsSDK(ctx, backend)
		if operationErr == nil {
			return output, nil
		}
		sdkErr = operationErr
	} else {
		sdkErr = err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	backend, commandErr := s.provider.Command(ctx, sessionID)
	if commandErr != nil {
		return "", errors.Join(sdkErr, commandErr)
	}
	snapshotter, ok := backend.(StatsSnapshotter)
	if !ok {
		return "", errors.Join(sdkErr, fmt.Errorf("%w: stats snapshot", ErrUnsupported))
	}
	output, commandErr := snapshotter.StatsSnapshot(ctx)
	if commandErr != nil {
		return "", errors.Join(sdkErr, commandErr)
	}
	return output, nil
}

func (s *Service) statsSDK(ctx context.Context, backend Backend) (string, error) {
	containers, err := backend.ListContainers(ctx, false)
	if err != nil {
		return "", err
	}
	if len(containers) == 0 {
		return "", nil
	}
	workerCount := min(s.config.StatsConcurrency, len(containers))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	lines := make([][]byte, len(containers))
	errs := make([]error, len(containers))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				stats, operationErr := backend.ContainerStats(ctx, containers[index].ID)
				if operationErr == nil {
					lines[index], operationErr = formatStats(stats, containers[index])
				}
				if operationErr != nil {
					errs[index] = operationErr
					cancel()
				}
			}
		}()
	}
	for index := range containers {
		select {
		case jobs <- index:
		case <-ctx.Done():
			errs[index] = ctx.Err()
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	var output bytes.Buffer
	for index, line := range lines {
		if errs[index] != nil {
			return "", fmt.Errorf("stats for %s: %w", containers[index].ID, errs[index])
		}
		if ctx.Err() != nil && len(line) == 0 {
			return "", ctx.Err()
		}
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.String(), nil
}

func (s *Service) Action(ctx context.Context, request ActionRequest) error {
	options := request.Options
	options.Container = strings.TrimSpace(options.Container)
	if options.Container == "" {
		return errors.New("docker container is required")
	}
	if !validAction(options.Action) {
		return fmt.Errorf("%w: %s", ErrInvalidAction, options.Action)
	}
	if options.Action == ActionRename && strings.TrimSpace(options.NewName) == "" {
		return errors.New("docker new name is required")
	}
	timeout := s.config.ActionTimeout
	if options.Action == ActionRename {
		timeout = s.config.ListTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backend, err := s.mutationBackend(ctx, request.SessionID, false)
	if err != nil {
		return err
	}
	started := s.config.Now()
	if err := backend.Action(ctx, options); err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{
		SessionID: request.SessionID,
		AssetID:   request.AssetID,
		Source:    auditSource(request.Source),
		Kind:      "docker_action",
		Payload: map[string]any{
			"container": options.Container,
			"action":    string(options.Action),
		},
		ExitCode: 0,
		Duration: s.config.Now().Sub(started),
	})
	return nil
}

func (s *Service) ImagePull(ctx context.Context, request ImagePullRequest) (string, error) {
	reference := strings.TrimSpace(request.Reference)
	if reference == "" {
		return "", errors.New("docker image reference is required")
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.PullTimeout)
	defer cancel()
	if !request.UseCommand && s.registryAuth == nil {
		command, commandErr := s.provider.Command(ctx, request.SessionID)
		if commandErr == nil {
			return command.PullImage(ctx, PullOptions{Reference: reference})
		}
		sdk, sdkErr := s.provider.SDK(ctx, request.SessionID)
		if sdkErr != nil {
			return "", errors.Join(commandErr, sdkErr)
		}
		return sdk.PullImage(ctx, PullOptions{Reference: reference})
	}
	if !request.UseCommand {
		if backend, err := s.provider.SDK(ctx, request.SessionID); err == nil {
			authSession := request.AuthSession
			if authSession == "" {
				authSession = request.SessionID
			}
			auth, authErr := s.registryAuth.RegistryAuth(ctx, authSession, reference)
			if authErr == nil {
				return backend.PullImage(ctx, PullOptions{Reference: reference, RegistryAuth: auth})
			}
		}
	}
	backend, err := s.provider.Command(ctx, request.SessionID)
	if err != nil {
		return "", err
	}
	return backend.PullImage(ctx, PullOptions{Reference: reference})
}

func (s *Service) ImageRemove(ctx context.Context, sessionID, reference string, force bool) error {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return errors.New("docker image reference is required")
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.ActionTimeout)
	defer cancel()
	backend, err := s.mutationBackend(ctx, sessionID, false)
	if err != nil {
		return err
	}
	return backend.RemoveImage(ctx, reference, force)
}

func (s *Service) ContainerListDir(ctx context.Context, sessionID, containerID, directory string) ([]string, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("docker directory is required")
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.ListTimeout)
	defer cancel()
	return readWithFallback(ctx, s.provider, sessionID, func(backend Backend) ([]string, error) {
		return backend.ListDir(ctx, containerID, directory)
	})
}

func (s *Service) Logs(ctx context.Context, request LogsRequest) (string, error) {
	if request.Tail < 0 {
		request.Tail = 200
	}
	request.Tail = min(request.Tail, 5000)
	ctx, cancel := context.WithTimeout(ctx, s.config.LogsTimeout)
	defer cancel()
	output, err := readWithFallback(ctx, s.provider, request.SessionID, func(backend Backend) (string, error) {
		stream, err := backend.OpenLogs(ctx, LogsOptions{
			Container: request.Container,
			Tail:      request.Tail,
			Follow:    false,
		})
		if err != nil {
			return "", err
		}
		defer stream.Reader.Close()
		return readDemultiplexed(stream.Reader, stream.TTY)
	})
	if err != nil || request.Grep == "" {
		return output, err
	}
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, request.Grep) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

func (s *Service) Exec(ctx context.Context, request ExecRequest) (ExecResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.LogsTimeout)
	defer cancel()
	backend, err := s.mutationBackend(ctx, request.SessionID, false)
	if err != nil {
		return ExecResult{}, err
	}
	session, err := backend.OpenExec(ctx, ExecOptions{
		Container: request.Container,
		Cmd:       []string{"sh", "-c", request.Command},
		TTY:       false,
	})
	if err != nil {
		return ExecResult{}, err
	}
	defer session.Close()
	_ = session.CloseWrite()
	output, readErr := readDemultiplexed(session, session.IsTTY())
	if readErr != nil {
		return ExecResult{}, readErr
	}
	exitCode, waitErr := session.Wait(ctx)
	if waitErr != nil {
		return ExecResult{}, waitErr
	}
	return ExecResult{Output: output, ExitCode: exitCode}, nil
}

func (s *Service) mutationBackend(ctx context.Context, sessionID string, forceCommand bool) (Backend, error) {
	if !forceCommand {
		backend, err := s.provider.SDK(ctx, sessionID)
		if err == nil {
			return backend, nil
		}
	}
	backend, commandErr := s.provider.Command(ctx, sessionID)
	if commandErr != nil {
		return nil, commandErr
	}
	return backend, nil
}

func (s *Service) recordAudit(ctx context.Context, event AuditEvent) {
	if s.auditor != nil {
		_ = s.auditor.RecordDocker(ctx, event)
	}
}

func (s *Service) CloseSession(sessionID string) error {
	streamErr := s.streams.closeSession(sessionID)
	if closer, ok := s.provider.(interface{ CloseSession(string) error }); ok {
		return errors.Join(streamErr, closer.CloseSession(sessionID))
	}
	return streamErr
}

func (s *Service) Close() error {
	streamErr := s.streams.closeAll()
	if closer, ok := s.provider.(interface{ Close() error }); ok {
		return errors.Join(streamErr, closer.Close())
	}
	return streamErr
}

func readWithFallback[T any](ctx context.Context, provider BackendProvider, sessionID string, operation func(Backend) (T, error)) (T, error) {
	var zero T
	var sdkErr error
	backend, err := provider.SDK(ctx, sessionID)
	if err == nil {
		var value T
		value, operationErr := operation(backend)
		if operationErr == nil {
			return value, nil
		}
		sdkErr = operationErr
	} else {
		sdkErr = err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	backend, commandErr := provider.Command(ctx, sessionID)
	if commandErr != nil {
		return zero, errors.Join(sdkErr, commandErr)
	}
	value, commandErr := operation(backend)
	if commandErr != nil {
		return zero, errors.Join(sdkErr, commandErr)
	}
	return value, nil
}

func readDemultiplexed(reader io.Reader, tty bool) (string, error) {
	var output bytes.Buffer
	var err error
	if tty {
		_, err = io.Copy(&output, reader)
	} else {
		_, err = stdcopy.StdCopy(&output, &output, reader)
	}
	return output.String(), err
}

func validAction(action Action) bool {
	switch action {
	case ActionStart, ActionStop, ActionRestart, ActionPause, ActionUnpause, ActionKill, ActionRemove, ActionRename:
		return true
	default:
		return false
	}
}

func auditSource(source string) string {
	if source == "" {
		return "user"
	}
	return source
}
