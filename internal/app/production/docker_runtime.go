package production

import (
	"context"
	"fmt"
	"io"

	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func newProductionDockerService(sessions *session.Manager, database *store.Store) *docker.Service {
	generations := docker.GenerationSourceFunc(func(sessionID string) (uint64, error) {
		connected, err := sessions.Session(sessionID)
		if err != nil {
			return 0, err
		}
		return connected.Info().Generation, nil
	})
	provider := docker.NewGenerationalProvider(generations,
		func(ctx context.Context, sessionID string, _ uint64) (docker.Backend, error) {
			connected, err := sessions.Session(sessionID)
			if err != nil {
				return nil, err
			}
			if connected.Asset().Kind == session.KindLocal {
				client, err := docker.NewClient(docker.ClientConfig{})
				if err != nil {
					return nil, err
				}
				return docker.NewMobyBackend(client), nil
			}
			transport, err := sessions.Transport(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			dialer, ok := transport.(base.Dialer)
			if !ok {
				return nil, fmt.Errorf("%w: SSH Docker dialer", base.ErrUnsupported)
			}
			client, err := docker.NewSSHStreamlocalClient("", dialer)
			if err != nil {
				return nil, err
			}
			return docker.NewMobyBackend(client), nil
		},
		func(ctx context.Context, sessionID string, _ uint64) (docker.Backend, error) {
			transport, err := sessions.Transport(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			runner := docker.CommandRunnerFunc(func(ctx context.Context, command string) (docker.CommandResult, error) {
				result, err := transport.Exec(ctx, command, base.ExecOptions{})
				return docker.CommandResult{
					Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode, Truncated: result.Truncated,
				}, err
			})
			opener := dockerCommandOpener{transport: transport}
			return docker.NewCommandBackend(runner, opener, docker.ShellPOSIX), nil
		},
	)
	return docker.NewService(provider, docker.WithAuditor(docker.AuditorFunc(func(ctx context.Context, event docker.AuditEvent) error {
		var sessionID, assetID *string
		if event.SessionID != "" {
			sessionID = &event.SessionID
		}
		if event.AssetID != "" {
			assetID = &event.AssetID
		}
		exitCode := int32(event.ExitCode)
		duration := event.Duration.Milliseconds()
		return database.AuditInsert(ctx, store.AuditInput{
			SessionID: sessionID, AssetID: assetID, Source: event.Source, Kind: event.Kind,
			Payload: event.Payload, ExitCode: &exitCode, DurationMS: &duration,
		})
	})))
}

type dockerCommandOpener struct {
	transport base.Transport
}

func (o dockerCommandOpener) OpenCommand(ctx context.Context, command string, width, height uint) (docker.CommandStream, error) {
	opener, ok := o.transport.(base.ExecStreamTransport)
	if !ok {
		return nil, fmt.Errorf("%w: exec stream", base.ErrUnsupported)
	}
	channel, err := opener.OpenExec(ctx, command, base.ExecOptions{})
	if err != nil {
		return nil, err
	}
	if width > 0 && height > 0 {
		if err := channel.Resize(ctx, uint32(width), uint32(height)); err != nil {
			_ = channel.Close()
			return nil, err
		}
	}
	return &dockerCommandChannel{Channel: channel}, nil
}

type dockerCommandChannel struct {
	base.Channel
}

func (c *dockerCommandChannel) Resize(ctx context.Context, width, height uint) error {
	return c.Channel.Resize(ctx, uint32(width), uint32(height))
}

func (c *dockerCommandChannel) Wait(ctx context.Context) (int, error) {
	err := c.Channel.Wait(ctx)
	if err == nil {
		return 0, nil
	}
	if code, ok := base.ExitCode(err); ok {
		return code, nil
	}
	return -1, err
}

func (c *dockerCommandChannel) NextOutput(ctx context.Context) (base.OutputEvent, error) {
	ordered, ok := c.Channel.(base.OrderedOutput)
	if !ok {
		return base.OutputEvent{}, base.ErrUnsupported
	}
	return ordered.NextOutput(ctx)
}

var _ docker.CommandStream = (*dockerCommandChannel)(nil)
var _ io.ReadWriteCloser = (*dockerCommandChannel)(nil)
