package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
)

type stubBackend struct {
	listContainers func(context.Context, bool) ([]container.Summary, error)
	inspect        func(context.Context, string) (json.RawMessage, error)
	stats          func(context.Context, string) (container.StatsResponse, error)
	listImages     func(context.Context) ([]image.Summary, error)
	action         func(context.Context, ActionOptions) error
	pull           func(context.Context, PullOptions) (string, error)
	removeImage    func(context.Context, string, bool) error
	openLogs       func(context.Context, LogsOptions) (LogStream, error)
	openExec       func(context.Context, ExecOptions) (ExecSession, error)
	listDir        func(context.Context, string, string) ([]string, error)
	closed         atomic.Int32
}

func (b *stubBackend) ListContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	if b.listContainers != nil {
		return b.listContainers(ctx, all)
	}
	return nil, ErrUnsupported
}
func (b *stubBackend) InspectContainer(ctx context.Context, id string) (json.RawMessage, error) {
	if b.inspect != nil {
		return b.inspect(ctx, id)
	}
	return nil, ErrUnsupported
}
func (b *stubBackend) ContainerStats(ctx context.Context, id string) (container.StatsResponse, error) {
	if b.stats != nil {
		return b.stats(ctx, id)
	}
	return container.StatsResponse{}, ErrUnsupported
}
func (b *stubBackend) ListImages(ctx context.Context) ([]image.Summary, error) {
	if b.listImages != nil {
		return b.listImages(ctx)
	}
	return nil, ErrUnsupported
}
func (b *stubBackend) Action(ctx context.Context, options ActionOptions) error {
	if b.action != nil {
		return b.action(ctx, options)
	}
	return ErrUnsupported
}
func (b *stubBackend) PullImage(ctx context.Context, options PullOptions) (string, error) {
	if b.pull != nil {
		return b.pull(ctx, options)
	}
	return "", ErrUnsupported
}
func (b *stubBackend) RemoveImage(ctx context.Context, reference string, force bool) error {
	if b.removeImage != nil {
		return b.removeImage(ctx, reference, force)
	}
	return ErrUnsupported
}
func (b *stubBackend) OpenLogs(ctx context.Context, options LogsOptions) (LogStream, error) {
	if b.openLogs != nil {
		return b.openLogs(ctx, options)
	}
	return LogStream{}, ErrUnsupported
}
func (b *stubBackend) OpenExec(ctx context.Context, options ExecOptions) (ExecSession, error) {
	if b.openExec != nil {
		return b.openExec(ctx, options)
	}
	return nil, ErrUnsupported
}
func (b *stubBackend) ListDir(ctx context.Context, id, path string) ([]string, error) {
	if b.listDir != nil {
		return b.listDir(ctx, id, path)
	}
	return nil, ErrUnsupported
}
func (b *stubBackend) Close() error {
	b.closed.Add(1)
	return nil
}

type stubProvider struct {
	sdk          Backend
	command      Backend
	sdkErr       error
	commandErr   error
	sdkCalls     atomic.Int32
	commandCalls atomic.Int32
}

func (p *stubProvider) SDK(context.Context, string) (Backend, error) {
	p.sdkCalls.Add(1)
	if p.sdkErr != nil {
		return nil, p.sdkErr
	}
	return p.sdk, nil
}
func (p *stubProvider) Command(context.Context, string) (Backend, error) {
	p.commandCalls.Add(1)
	if p.commandErr != nil {
		return nil, p.commandErr
	}
	return p.command, nil
}

func TestServiceReadFallbackAndMutationNoReplay(t *testing.T) {
	commandRunner := &recordingRunner{result: CommandResult{Stdout: "{\"ID\":\"abc\",\"Names\":\"cli\",\"State\":\"running\"}\n"}}
	command := NewCommandBackend(commandRunner, nil, ShellPOSIX)
	sdk := &stubBackend{listContainers: func(context.Context, bool) ([]container.Summary, error) {
		return nil, errors.New("socket unavailable")
	}}
	provider := &stubProvider{sdk: sdk, command: command}
	service := NewService(provider)
	containers, err := service.PS(t.Context(), "s1")
	if err != nil || len(containers) != 1 || containers[0].Name != "cli" {
		t.Fatalf("fallback PS = %+v, %v", containers, err)
	}
	var commandActions atomic.Int32
	sdk.action = func(context.Context, ActionOptions) error { return errors.New("connection lost after request") }
	commandBackend := &stubBackend{action: func(context.Context, ActionOptions) error {
		commandActions.Add(1)
		return nil
	}}
	provider.command = commandBackend
	if err := service.Action(t.Context(), ActionRequest{SessionID: "s1", Options: ActionOptions{Container: "c1", Action: ActionRestart}}); err == nil {
		t.Fatal("expected mutation error")
	}
	if commandActions.Load() != 0 {
		t.Fatal("ambiguous mutation was replayed on command backend")
	}
}

func TestServicePullAndExecNeverReplayAfterRequest(t *testing.T) {
	var sdkPulls, sdkExecs, commandPulls, commandExecs atomic.Int32
	sdk := &stubBackend{
		pull: func(context.Context, PullOptions) (string, error) {
			sdkPulls.Add(1)
			return "", errors.New("ambiguous pull result")
		},
		openExec: func(context.Context, ExecOptions) (ExecSession, error) {
			sdkExecs.Add(1)
			return nil, errors.New("ambiguous exec result")
		},
	}
	command := &stubBackend{
		pull: func(context.Context, PullOptions) (string, error) {
			commandPulls.Add(1)
			return "cli pull", nil
		},
		openExec: func(context.Context, ExecOptions) (ExecSession, error) {
			commandExecs.Add(1)
			return nil, nil
		},
	}
	provider := &stubProvider{sdk: sdk, command: command}
	service := NewService(provider, WithRegistryAuth(RegistryAuthProviderFunc(func(context.Context, string, string) (string, error) {
		return "", nil
	})))
	if _, err := service.ImagePull(t.Context(), ImagePullRequest{SessionID: "s1", Reference: "app:v1"}); err == nil {
		t.Fatal("expected pull error")
	}
	if _, err := service.Exec(t.Context(), ExecRequest{SessionID: "s1", Container: "c1", Command: "true"}); err == nil {
		t.Fatal("expected exec error")
	}
	if commandPulls.Load() != 0 || commandExecs.Load() != 0 {
		t.Fatal("mutation was replayed")
	}
	provider.sdkErr = ErrNoBackend
	output, err := service.ImagePull(t.Context(), ImagePullRequest{SessionID: "s1", Reference: "app:v1"})
	if err != nil || output != "cli pull" || commandPulls.Load() != 1 {
		t.Fatalf("pre-connect command fallback = %q, %v", output, err)
	}
}

func TestServicePullWithoutAuthProviderUsesCLICredentials(t *testing.T) {
	var sdkPulls, commandPulls atomic.Int32
	sdk := &stubBackend{pull: func(context.Context, PullOptions) (string, error) {
		sdkPulls.Add(1)
		return "sdk", nil
	}}
	command := &stubBackend{pull: func(context.Context, PullOptions) (string, error) {
		commandPulls.Add(1)
		return "cli", nil
	}}
	service := NewService(&stubProvider{sdk: sdk, command: command})
	output, err := service.ImagePull(t.Context(), ImagePullRequest{SessionID: "s1", Reference: "private/app:v1"})
	if err != nil || output != "cli" || sdkPulls.Load() != 0 || commandPulls.Load() != 1 {
		t.Fatalf("output = %q, err = %v, sdk = %d, command = %d", output, err, sdkPulls.Load(), commandPulls.Load())
	}
}

func TestServiceStatsBoundedAndOrdered(t *testing.T) {
	const count = 8
	containers := make([]container.Summary, count)
	for index := range containers {
		containers[index] = container.Summary{ID: strings.Repeat(string(rune('a'+index)), 16), Names: []string{"/c"}}
	}
	var active, maximum atomic.Int32
	backend := &stubBackend{
		listContainers: func(context.Context, bool) ([]container.Summary, error) { return containers, nil },
		stats: func(ctx context.Context, id string) (container.StatsResponse, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				peak := maximum.Load()
				if current <= peak || maximum.CompareAndSwap(peak, current) {
					break
				}
			}
			select {
			case <-time.After(15 * time.Millisecond):
			case <-ctx.Done():
				return container.StatsResponse{}, ctx.Err()
			}
			return container.StatsResponse{ID: id, Name: "/" + id[:1]}, nil
		},
	}
	service := NewService(&stubProvider{sdk: backend}, WithConfig(Config{StatsConcurrency: 3}))
	output, err := service.Stats(t.Context(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != count || maximum.Load() > 3 {
		t.Fatalf("lines = %d, concurrency = %d", len(lines), maximum.Load())
	}
	for index, line := range lines {
		var value statsLine
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		if value.Name != string(rune('a'+index)) {
			t.Fatalf("line %d is out of order: %+v", index, value)
		}
	}
}

func TestServiceLogsExecAndOtherCapabilities(t *testing.T) {
	multiplexed := append([]byte{1, 0, 0, 0, 0, 0, 0, 5}, []byte("one\nx")...)
	multiplexed = append(multiplexed, []byte{2, 0, 0, 0, 0, 0, 0, 4}...)
	multiplexed = append(multiplexed, []byte("two\n")...)
	execSession := &bufferExecSession{reader: io.NopCloser(bytes.NewReader(multiplexed)), tty: false}
	backend := &stubBackend{
		listContainers: func(context.Context, bool) ([]container.Summary, error) {
			return []container.Summary{{ID: "c1", Names: []string{"/one"}, State: container.StateRunning}}, nil
		},
		listImages: func(context.Context) ([]image.Summary, error) {
			return []image.Summary{{ID: "sha256:abcdef0123456789", RepoTags: []string{"app:v1"}}}, nil
		},
		inspect: func(context.Context, string) (json.RawMessage, error) { return json.RawMessage(`{"Id":"c1"}`), nil },
		openLogs: func(context.Context, LogsOptions) (LogStream, error) {
			return LogStream{Reader: io.NopCloser(strings.NewReader("alpha\nbeta\n")), TTY: true}, nil
		},
		openExec: func(context.Context, ExecOptions) (ExecSession, error) { return execSession, nil },
		listDir:  func(context.Context, string, string) ([]string, error) { return []string{".", "..", "a b"}, nil },
	}
	service := NewService(&stubProvider{sdk: backend})
	overview, err := service.Overview(t.Context(), "s1")
	if err != nil || overview.HostStats.ContainersRunning != 1 || overview.HostStats.Images != 1 {
		t.Fatalf("overview = %+v, %v", overview, err)
	}
	if _, err := service.Inspect(t.Context(), "s1", "c1"); err != nil {
		t.Fatal(err)
	}
	if err := service.ImageRemove(t.Context(), "s1", "app:v1", false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("remove error = %v", err)
	}
	entries, err := service.ContainerListDir(t.Context(), "s1", "c1", "/")
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	logs, err := service.Logs(t.Context(), LogsRequest{SessionID: "s1", Container: "c1", Tail: 200, Grep: "alp"})
	if err != nil || logs != "alpha" {
		t.Fatalf("logs = %q, %v", logs, err)
	}
	result, err := service.Exec(t.Context(), ExecRequest{SessionID: "s1", Container: "c1", Command: "test"})
	if err != nil || result.Output != "one\nxtwo\n" || result.ExitCode != 0 {
		t.Fatalf("exec = %+v, %v", result, err)
	}
}

type bufferExecSession struct {
	reader   io.ReadCloser
	tty      bool
	write    bytes.Buffer
	resizes  [][2]uint
	closed   bool
	exitCode int
}

func (s *bufferExecSession) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *bufferExecSession) Write(p []byte) (int, error) { return s.write.Write(p) }
func (s *bufferExecSession) Close() error {
	s.closed = true
	return s.reader.Close()
}
func (s *bufferExecSession) CloseWrite() error { return nil }
func (s *bufferExecSession) Resize(_ context.Context, width, height uint) error {
	s.resizes = append(s.resizes, [2]uint{width, height})
	return nil
}
func (s *bufferExecSession) Wait(context.Context) (int, error) { return s.exitCode, nil }
func (s *bufferExecSession) IsTTY() bool                       { return s.tty }

func TestServiceAuditAndRegistryAuth(t *testing.T) {
	var events []AuditEvent
	var mu sync.Mutex
	var authValue string
	backend := &stubBackend{
		action: func(context.Context, ActionOptions) error { return nil },
		pull: func(_ context.Context, options PullOptions) (string, error) {
			authValue = options.RegistryAuth
			return "ok", nil
		},
	}
	service := NewService(&stubProvider{sdk: backend},
		WithAuditor(AuditorFunc(func(_ context.Context, event AuditEvent) error {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return nil
		})),
		WithRegistryAuth(RegistryAuthProviderFunc(func(context.Context, string, string) (string, error) {
			return "private-auth", nil
		})),
	)
	if err := service.Action(t.Context(), ActionRequest{SessionID: "s1", Source: "ai", Options: ActionOptions{Container: "c1", Action: ActionStart}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImagePull(t.Context(), ImagePullRequest{SessionID: "s1", Reference: "private/app:v1"}); err != nil {
		t.Fatal(err)
	}
	if authValue != "private-auth" || len(events) != 1 || events[0].Source != "ai" || events[0].Kind != "docker_action" {
		t.Fatalf("auth = %q, events = %+v", authValue, events)
	}
}
