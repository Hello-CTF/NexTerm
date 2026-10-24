package production

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/device"
)

type productionDockerProvider struct {
	sessions *session.Manager
	pool     *docker.GenerationalProvider
}

func (p *productionDockerProvider) authorize(ctx context.Context, sessionID string) error {
	_, err := p.sessions.SessionFor(productionSessionRequestContext(ctx), sessionID)
	return err
}

func (p *productionDockerProvider) SDK(ctx context.Context, sessionID string) (docker.Backend, error) {
	if err := p.authorize(ctx, sessionID); err != nil {
		return nil, err
	}
	return p.pool.SDK(ctx, sessionID)
}

func (p *productionDockerProvider) Command(ctx context.Context, sessionID string) (docker.Backend, error) {
	if err := p.authorize(ctx, sessionID); err != nil {
		return nil, err
	}
	return p.pool.Command(ctx, sessionID)
}

func (p *productionDockerProvider) CloseSession(sessionID string) error {
	return p.pool.CloseSession(sessionID)
}

func (p *productionDockerProvider) Close() error {
	return p.pool.Close()
}

func newProductionDockerService(sessions *session.Manager, database *store.Store) *docker.Service {
	generations := docker.GenerationSourceFunc(func(sessionID string) (uint64, error) {
		connected, err := sessions.Session(sessionID)
		if err != nil {
			return 0, err
		}
		return connected.Info().Generation, nil
	})
	pool := docker.NewGenerationalProvider(generations,
		func(ctx context.Context, sessionID string, _ uint64) (docker.Backend, error) {
			ctx = productionSessionRequestContext(ctx)
			connected, err := sessions.SessionFor(ctx, sessionID)
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
			transport, err := sessions.TransportFor(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			if connected.Asset().Kind == session.KindDevice {
				return newDeviceDockerSDKBackend(ctx, transport, deviceDockerSocket)
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
			ctx = productionSessionRequestContext(ctx)
			connected, err := sessions.SessionFor(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			transport, err := sessions.TransportFor(ctx, sessionID)
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
			shell := dockerFallbackShell(connected.Asset())
			// Device agents know their real platform; windows devices must use the PowerShell quoting the
			// transport's exec wrapper speaks (the asset-level fallback defaults to POSIX for devices).
			if deviceTransport, ok := transport.(*device.Transport); ok && deviceTransport.Platform() == "windows" {
				shell = docker.ShellPowerShell
			}
			return docker.NewCommandBackend(runner, opener, shell), nil
		},
	)
	provider := &productionDockerProvider{sessions: sessions, pool: pool}
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

func dockerFallbackShell(asset session.Asset) docker.Shell {
	return dockerFallbackShellForGOOS(asset, runtime.GOOS)
}

func dockerFallbackShellForGOOS(asset session.Asset, goos string) docker.Shell {
	if asset.Kind == session.KindWinRM {
		return docker.ShellPowerShell
	}
	var options struct {
		Shell string `json:"shell"`
	}
	if raw, err := json.Marshal(asset.Options); err == nil {
		_ = json.Unmarshal(raw, &options)
	}
	if options.Shell == "" {
		if asset.Kind == session.KindLocal && goos == "windows" {
			return docker.ShellPowerShell
		}
		return docker.ShellPOSIX
	}
	if windowsShell(options.Shell) {
		return docker.ShellPowerShell
	}
	return docker.ShellPOSIX
}

func windowsShell(shell string) bool {
	name := strings.ToLower(strings.TrimSpace(shell))
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	name = strings.TrimSuffix(name, ".exe")
	return name == "powershell" || name == "pwsh"
}

type dockerCommandOpener struct {
	transport base.Transport
}

func (o dockerCommandOpener) OpenCommand(ctx context.Context, command string, width, height uint) (docker.CommandStream, error) {
	// Device exec streams have no stdin or PTY, so they cannot back interactive container exec.
	if _, isDevice := o.transport.(*device.Transport); isDevice {
		return nil, deviceDockerStreamsUnavailable{}
	}
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

const (
	deviceDockerSocket       = "/var/run/docker.sock"
	deviceDockerProbeTimeout = 15 * time.Second
)

// newDeviceDockerSDKBackend reaches the device's Docker Engine socket through a forward bridge, the same way the SSH
// path uses a streamlocal channel, so container exec, log streaming and stats work without a streamed exec channel.
// Windows agents expose Docker through a named pipe that the forward bridge cannot dial, so they keep the CLI backend.
func newDeviceDockerSDKBackend(ctx context.Context, transport base.Transport, socket string) (docker.Backend, error) {
	deviceTransport, ok := transport.(*device.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: device Docker dialer", base.ErrUnsupported)
	}
	if deviceTransport.Platform() == "windows" {
		return nil, fmt.Errorf("%w: Windows 设备代理的 Docker 使用命名管道，无法通过桥接访问", base.ErrUnsupported)
	}
	// The client dials lazily, so probe the socket once: an unreachable socket must fall back to the CLI backend
	// instead of failing every SDK mutation later.
	probeCtx, cancel := context.WithTimeout(ctx, deviceDockerProbeTimeout)
	defer cancel()
	conn, err := deviceTransport.DialContext(probeCtx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("%w: 无法连接设备上的 Docker 套接字: %v", base.ErrUnsupported, err)
	}
	_ = conn.Close()
	client, err := docker.NewSSHStreamlocalClient(socket, deviceTransport)
	if err != nil {
		return nil, err
	}
	return docker.NewMobyBackend(client), nil
}

// deviceDockerStreamsUnavailable is the user-facing reason a device cannot stream container exec or logs: both need
// the Docker socket bridge (the CLI fallback has no streamed exec channel on devices).
type deviceDockerStreamsUnavailable struct{}

func (deviceDockerStreamsUnavailable) Error() string {
	return "无法通过设备代理连接 Docker 套接字，容器终端与日志流暂不可用：请确认设备上 Docker 正在运行、代理账号有权访问 /var/run/docker.sock，并把设备代理升级到最新版；Windows 设备代理暂不支持"
}

func (deviceDockerStreamsUnavailable) Unwrap() error { return docker.ErrUnsupported }
