package production

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
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
	// The SDK factory records why a device's Docker socket was unreachable so the CLI fallback can name the cause
	// instead of a generic "unavailable" message.
	var deviceProbeFailures sync.Map
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
				backend, err := newDeviceDockerSDKBackend(ctx, transport, "")
				if err != nil {
					deviceProbeFailures.Store(sessionID, err)
				} else {
					deviceProbeFailures.Delete(sessionID)
				}
				return backend, err
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
			if cause, ok := deviceProbeFailures.Load(sessionID); ok {
				opener.cause, _ = cause.(error)
			}
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
	// cause is why the device Docker socket bridge could not be opened, when known.
	cause error
}

func (o dockerCommandOpener) OpenCommand(ctx context.Context, command string, width, height uint) (docker.CommandStream, error) {
	// Device exec streams have no stdin or PTY, so they cannot back interactive container exec.
	if _, isDevice := o.transport.(*device.Transport); isDevice {
		return nil, deviceDockerStreamsUnavailable{cause: o.cause}
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
	deviceDockerProbeTimeout = 5 * time.Second
	deviceDockerPipe         = "//./pipe/docker_engine"
)

// deviceDockerSocketCandidates are tried in order on non-Windows devices. The agent expands "~" and
// "$XDG_RUNTIME_DIR" itself, so Docker Desktop, Colima, OrbStack, rootless Docker and Podman sockets are reachable
// even though the default /var/run/docker.sock is missing or inaccessible to the agent account.
var deviceDockerSocketCandidates = []string{
	"/var/run/docker.sock",
	"~/.docker/run/docker.sock",
	"~/.docker/desktop/docker.sock",
	"~/.orbstack/run/docker.sock",
	"~/.colima/default/docker.sock",
	"$XDG_RUNTIME_DIR/docker.sock",
	"$XDG_RUNTIME_DIR/podman/podman.sock",
}

// newDeviceDockerSDKBackend reaches the device's Docker Engine through a forward bridge, the same way the SSH path
// uses a streamlocal channel, so container exec, log streaming and stats work without a streamed exec channel.
// An empty socket probes the known candidates; Windows devices dial the Docker Engine named pipe.
func newDeviceDockerSDKBackend(ctx context.Context, transport base.Transport, socket string) (docker.Backend, error) {
	deviceTransport, ok := transport.(*device.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: device Docker dialer", base.ErrUnsupported)
	}
	// The client dials lazily, so probe once: an unreachable engine must fall back to the CLI backend instead of
	// failing every SDK mutation later.
	if deviceTransport.Platform() == "windows" {
		if err := probeDeviceDial(ctx, deviceTransport, "npipe", deviceDockerPipe); err != nil {
			return nil, fmt.Errorf("%w: 无法连接设备上的 Docker 命名管道 %s（升级设备代理后重试）: %v", base.ErrUnsupported, deviceDockerPipe, err)
		}
		client, err := docker.NewNamedPipeClient(deviceDockerPipe, deviceTransport)
		if err != nil {
			return nil, err
		}
		return docker.NewMobyBackend(client), nil
	}
	candidates := deviceDockerSocketCandidates
	if socket != "" {
		candidates = []string{socket}
	}
	var failures []string
	for _, candidate := range candidates {
		err := probeDeviceDial(ctx, deviceTransport, "unix", candidate)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		client, err := docker.NewSSHStreamlocalClient(candidate, deviceTransport)
		if err != nil {
			return nil, err
		}
		return docker.NewMobyBackend(client), nil
	}
	return nil, fmt.Errorf("%w: 无法连接设备上的 Docker 套接字（%s）", base.ErrUnsupported, strings.Join(failures, "; "))
}

func probeDeviceDial(ctx context.Context, transport *device.Transport, network, address string) error {
	probeCtx, cancel := context.WithTimeout(ctx, deviceDockerProbeTimeout)
	defer cancel()
	conn, err := transport.DialContext(probeCtx, network, address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// deviceDockerStreamsUnavailable is the user-facing reason a device cannot stream container exec or logs: both need
// the Docker socket bridge (the CLI fallback has no streamed exec channel on devices).
type deviceDockerStreamsUnavailable struct{ cause error }

func (e deviceDockerStreamsUnavailable) Error() string {
	message := "无法通过设备代理连接 Docker，容器终端与日志流暂不可用：请确认设备上 Docker（或 Docker Desktop / Colima / OrbStack）正在运行、代理账号有权访问其套接字，并把设备代理升级到最新版"
	if e.cause != nil {
		message += "。原因：" + e.cause.Error()
	}
	return message
}

func (deviceDockerStreamsUnavailable) Unwrap() error { return docker.ErrUnsupported }
