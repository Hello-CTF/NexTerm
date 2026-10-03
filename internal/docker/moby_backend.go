package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

type MobyBackend struct {
	client *client.Client
}

func NewMobyBackend(cli *client.Client) *MobyBackend {
	return &MobyBackend{client: cli}
}

func (b *MobyBackend) ListContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	result, err := b.client.ContainerList(ctx, client.ContainerListOptions{All: all})
	return result.Items, err
}

func (b *MobyBackend) InspectContainer(ctx context.Context, id string) (json.RawMessage, error) {
	result, err := b.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	if len(result.Raw) == 0 {
		return json.Marshal(result.Container)
	}
	return result.Raw, nil
}

func (b *MobyBackend) ContainerStats(ctx context.Context, id string) (container.StatsResponse, error) {
	result, err := b.client.ContainerStats(ctx, id, client.ContainerStatsOptions{
		Stream:                false,
		IncludePreviousSample: true,
	})
	if err != nil {
		return container.StatsResponse{}, err
	}
	defer result.Body.Close()
	var stats container.StatsResponse
	err = json.NewDecoder(result.Body).Decode(&stats)
	return stats, err
}

func (b *MobyBackend) ListImages(ctx context.Context) ([]image.Summary, error) {
	result, err := b.client.ImageList(ctx, client.ImageListOptions{})
	return result.Items, err
}

func (b *MobyBackend) Action(ctx context.Context, options ActionOptions) error {
	id := options.Container
	switch options.Action {
	case ActionStart:
		_, err := b.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
		return err
	case ActionStop:
		_, err := b.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: options.StopTimeout})
		return err
	case ActionRestart:
		_, err := b.client.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: options.StopTimeout})
		return err
	case ActionPause:
		_, err := b.client.ContainerPause(ctx, id, client.ContainerPauseOptions{})
		return err
	case ActionUnpause:
		_, err := b.client.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
		return err
	case ActionKill:
		_, err := b.client.ContainerKill(ctx, id, client.ContainerKillOptions{})
		return err
	case ActionRemove:
		_, err := b.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: options.Force})
		return err
	case ActionRename:
		_, err := b.client.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: options.NewName})
		return err
	default:
		return fmt.Errorf("%w: %s", ErrInvalidAction, options.Action)
	}
}

func (b *MobyBackend) PullImage(ctx context.Context, options PullOptions) (string, error) {
	response, err := b.client.ImagePull(ctx, options.Reference, client.ImagePullOptions{
		RegistryAuth: options.RegistryAuth,
	})
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	for message, streamErr := range response.JSONMessages(ctx) {
		if streamErr != nil {
			return output.String(), streamErr
		}
		if err := json.NewEncoder(&output).Encode(message); err != nil {
			return output.String(), err
		}
		if message.Error != nil {
			return output.String(), message.Error
		}
	}
	return output.String(), nil
}

func (b *MobyBackend) RemoveImage(ctx context.Context, reference string, force bool) error {
	_, err := b.client.ImageRemove(ctx, reference, client.ImageRemoveOptions{Force: force})
	return err
}

func (b *MobyBackend) OpenLogs(ctx context.Context, options LogsOptions) (LogStream, error) {
	inspect, err := b.client.ContainerInspect(ctx, options.Container, client.ContainerInspectOptions{})
	if err != nil {
		return LogStream{}, err
	}
	tail := "all"
	if options.Tail >= 0 {
		tail = fmt.Sprintf("%d", options.Tail)
	}
	body, err := b.client.ContainerLogs(ctx, options.Container, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     options.Follow,
		Tail:       tail,
		Since:      options.Since,
	})
	if err != nil {
		return LogStream{}, err
	}
	tty := inspect.Container.Config != nil && inspect.Container.Config.Tty
	return LogStream{Reader: body, TTY: tty}, nil
}

func (b *MobyBackend) OpenExec(ctx context.Context, options ExecOptions) (ExecSession, error) {
	created, err := b.client.ExecCreate(ctx, options.Container, client.ExecCreateOptions{
		User:         options.User,
		TTY:          options.TTY,
		ConsoleSize:  client.ConsoleSize{Height: options.Height, Width: options.Width},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          options.Env,
		WorkingDir:   options.WorkDir,
		Cmd:          options.Cmd,
	})
	if err != nil {
		return nil, err
	}
	attached, err := b.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{
		TTY:         options.TTY,
		ConsoleSize: client.ConsoleSize{Height: options.Height, Width: options.Width},
	})
	if err != nil {
		return nil, err
	}
	return &mobyExecSession{
		client:   b.client,
		id:       created.ID,
		response: attached.HijackedResponse,
		tty:      options.TTY,
	}, nil
}

func (b *MobyBackend) ListDir(ctx context.Context, id, directory string) ([]string, error) {
	result, err := execWithSession(ctx, b, ExecOptions{
		Container: id,
		Cmd:       []string{"ls", "-1a", "--", directory},
		TTY:       false,
	})
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("docker ls exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	lines := strings.Split(result.Output, "\n")
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			entries = append(entries, line)
		}
	}
	return entries, nil
}

func (b *MobyBackend) Close() error {
	return b.client.Close()
}

type mobyExecSession struct {
	client   *client.Client
	id       string
	response client.HijackedResponse
	tty      bool
	close    sync.Once
}

func (s *mobyExecSession) Read(buffer []byte) (int, error) {
	return s.response.Reader.Read(buffer)
}

func (s *mobyExecSession) Write(buffer []byte) (int, error) {
	return s.response.Conn.Write(buffer)
}

func (s *mobyExecSession) Close() error {
	s.close.Do(func() {
		_ = s.response.CloseWrite()
		s.response.Close()
	})
	return nil
}

func (s *mobyExecSession) CloseWrite() error {
	return s.response.CloseWrite()
}

func (s *mobyExecSession) Resize(ctx context.Context, width, height uint) error {
	_, err := s.client.ExecResize(ctx, s.id, client.ExecResizeOptions{Width: width, Height: height})
	return err
}

func (s *mobyExecSession) Wait(ctx context.Context) (int, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := s.client.ExecInspect(ctx, s.id, client.ExecInspectOptions{})
		if err != nil {
			return -1, err
		}
		if !result.Running {
			return result.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *mobyExecSession) IsTTY() bool {
	return s.tty
}
