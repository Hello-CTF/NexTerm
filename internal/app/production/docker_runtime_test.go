package production

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type dockerShellTransport struct {
	kind     string
	commands []string
}

func (t *dockerShellTransport) Kind() string       { return t.kind }
func (t *dockerShellTransport) Generation() uint64 { return 1 }
func (t *dockerShellTransport) IsAlive() bool      { return true }
func (t *dockerShellTransport) Close() error       { return nil }
func (t *dockerShellTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}

func (t *dockerShellTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	t.commands = append(t.commands, command)
	return base.ExecResult{}, nil
}

func TestDockerFallbackShellSelection(t *testing.T) {
	const (
		powerShellCommand = `docker 'restart' 'a''b'`
		posixCommand      = `docker 'restart' 'a'"'"'b'`
	)
	for _, testCase := range []struct {
		name        string
		asset       session.Asset
		transportID string
		want        string
	}{
		{name: "winrm", asset: session.Asset{ID: "a-winrm", Kind: session.KindWinRM}, transportID: "winrm", want: powerShellCommand},
		{name: "windows ssh powershell", asset: session.Asset{ID: "a-win-ps", Kind: session.KindSSH, Options: map[string]any{"shell": "powershell.exe"}}, transportID: "ssh", want: powerShellCommand},
		{name: "windows ssh pwsh path", asset: session.Asset{ID: "a-win-pwsh", Kind: session.KindSSH, Options: map[string]any{"shell": `C:\Program Files\PowerShell\7\pwsh.exe`}}, transportID: "ssh", want: powerShellCommand},
		{name: "windows docker asset", asset: session.Asset{ID: "a-win-docker", Kind: session.KindDocker, Options: map[string]any{"shell": "pwsh"}}, transportID: "ssh", want: powerShellCommand},
		{name: "unix ssh default", asset: session.Asset{ID: "a-unix", Kind: session.KindSSH}, transportID: "ssh", want: posixCommand},
		{name: "unix ssh bash", asset: session.Asset{ID: "a-bash", Kind: session.KindSSH, Options: map[string]any{"shell": "/bin/bash"}}, transportID: "ssh", want: posixCommand},
		{name: "device default", asset: session.Asset{ID: "device/d-1", Kind: session.KindDevice, Options: map[string]any{"deviceId": "d-1"}}, transportID: "device", want: posixCommand},
		{name: "device explicit pwsh", asset: session.Asset{ID: "device/d-2", Kind: session.KindDevice, Options: map[string]any{"deviceId": "d-2", "shell": "pwsh"}}, transportID: "device", want: powerShellCommand},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &dockerShellTransport{kind: testCase.transportID}
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return transport, nil
			})
			manager := session.NewManager(session.Config{Connector: connector, OpenAccess: true})
			database, err := store.OpenInMemory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			service := newProductionDockerService(manager, database)
			connected, err := manager.Connect(t.Context(), testCase.asset)
			if err != nil {
				t.Fatal(err)
			}
			err = service.Action(t.Context(), docker.ActionRequest{
				SessionID: connected.ID,
				AssetID:   testCase.asset.ID,
				Source:    "user",
				Options:   docker.ActionOptions{Container: "a'b", Action: docker.ActionRestart},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(transport.commands) != 1 || transport.commands[0] != testCase.want {
				t.Fatalf("commands = %q, want %q", transport.commands, testCase.want)
			}
		})
	}
}

func TestWindowsShellDetection(t *testing.T) {
	for _, testCase := range []struct {
		shell string
		want  bool
	}{
		{shell: "powershell", want: true},
		{shell: "powershell.exe", want: true},
		{shell: "PowerShell.EXE", want: true},
		{shell: "pwsh", want: true},
		{shell: `C:\Program Files\PowerShell\7\pwsh.exe`, want: true},
		{shell: "cmd.exe", want: false},
		{shell: "cmd", want: false},
		{shell: "/bin/bash", want: false},
		{shell: "/usr/bin/pwsh", want: true},
		{shell: "", want: false},
		{shell: "bash", want: false},
	} {
		if got := windowsShell(testCase.shell); got != testCase.want {
			t.Fatalf("windowsShell(%q) = %v, want %v", testCase.shell, got, testCase.want)
		}
	}
}

func TestDockerFallbackShellLocalDefault(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		goos    string
		options any
		want    docker.Shell
	}{
		{name: "windows default", goos: "windows", want: docker.ShellPowerShell},
		{name: "linux default", goos: "linux", want: docker.ShellPOSIX},
		{name: "darwin default", goos: "darwin", want: docker.ShellPOSIX},
		{name: "windows explicit powershell", goos: "windows", options: map[string]any{"shell": "powershell.exe"}, want: docker.ShellPowerShell},
		{name: "windows explicit pwsh", goos: "windows", options: map[string]any{"shell": "pwsh"}, want: docker.ShellPowerShell},
		{name: "windows explicit bash", goos: "windows", options: map[string]any{"shell": `C:\msys64\bin\bash.exe`}, want: docker.ShellPOSIX},
		{name: "windows explicit cmd", goos: "windows", options: map[string]any{"shell": "cmd.exe"}, want: docker.ShellPOSIX},
		{name: "linux explicit pwsh", goos: "linux", options: map[string]any{"shell": "pwsh"}, want: docker.ShellPowerShell},
		{name: "linux explicit bash", goos: "linux", options: map[string]any{"shell": "/bin/bash"}, want: docker.ShellPOSIX},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			asset := session.Asset{ID: "a-local", Kind: session.KindLocal, Options: testCase.options}
			if got := dockerFallbackShellForGOOS(asset, testCase.goos); got != testCase.want {
				t.Fatalf("dockerFallbackShellForGOOS(%v, %q) = %v, want %v", testCase.options, testCase.goos, got, testCase.want)
			}
		})
	}
}

type dockerDeviceTransport struct {
	commands []string
	dialed   bool
	execErr  error
	respond  func(command string) base.ExecResult
}

func (t *dockerDeviceTransport) Kind() string       { return "device" }
func (t *dockerDeviceTransport) Generation() uint64 { return 1 }
func (t *dockerDeviceTransport) IsAlive() bool      { return true }
func (t *dockerDeviceTransport) Close() error       { return nil }
func (t *dockerDeviceTransport) Ping(context.Context) (time.Duration, error) {
	return 0, nil
}

func (t *dockerDeviceTransport) Exec(_ context.Context, command string, _ base.ExecOptions) (base.ExecResult, error) {
	t.commands = append(t.commands, command)
	if t.execErr != nil {
		return base.ExecResult{}, t.execErr
	}
	return t.respond(command), nil
}

func (t *dockerDeviceTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	t.dialed = true
	return nil, base.ErrUnsupported
}

var _ base.Dialer = (*dockerDeviceTransport)(nil)

func newDockerDeviceTestService(t *testing.T, transport base.Transport) (*docker.Service, string) {
	t.Helper()
	connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
		return transport, nil
	})
	manager := session.NewManager(session.Config{Connector: connector, OpenAccess: true})
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	service := newProductionDockerService(manager, database)
	connected, err := manager.Connect(t.Context(), session.Asset{
		ID: "device/d-1", Kind: session.KindDevice, Options: map[string]any{"deviceId": "d-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, connected.ID
}

func TestDockerDeviceCommandBackend(t *testing.T) {
	transport := &dockerDeviceTransport{
		respond: func(command string) base.ExecResult {
			switch {
			case strings.HasPrefix(command, `docker 'ps' `):
				return base.ExecResult{Stdout: "{\"ID\":\"c1\",\"Names\":\"web\",\"Image\":\"nginx:latest\",\"State\":\"running\",\"Status\":\"Up 2 hours\",\"Ports\":\"\",\"Labels\":\"\"}\n"}
			case strings.HasPrefix(command, `docker 'images' `):
				return base.ExecResult{Stdout: "{\"ID\":\"i1\",\"Repository\":\"nginx\",\"Tag\":\"latest\",\"Size\":\"187MB\",\"CreatedSince\":\"2 weeks ago\"}\n"}
			case strings.HasPrefix(command, `docker 'inspect' `):
				return base.ExecResult{Stdout: "{\"Id\":\"c1\",\"State\":{\"Status\":\"running\"}}"}
			case strings.HasPrefix(command, `docker 'stats' `):
				return base.ExecResult{Stdout: "{\"CPUPerc\":\"0.5%\"}\n"}
			case strings.HasPrefix(command, `docker 'pull' `):
				return base.ExecResult{Stdout: "pulled\n"}
			case strings.HasPrefix(command, `docker 'exec' `):
				return base.ExecResult{Stdout: "hi\n"}
			default:
				return base.ExecResult{}
			}
		},
	}
	service, sessionID := newDockerDeviceTestService(t, transport)
	containers, err := service.PS(t.Context(), sessionID)
	if err != nil || len(containers) != 1 || containers[0].Name != "web" {
		t.Fatalf("PS = %+v, %v", containers, err)
	}
	images, err := service.Images(t.Context(), sessionID)
	if err != nil || len(images) != 1 || images[0].Repository != "nginx" || images[0].Tag != "latest" {
		t.Fatalf("Images = %+v, %v", images, err)
	}
	inspect, err := service.Inspect(t.Context(), sessionID, "web")
	if err != nil || string(inspect) != `{"Id":"c1","State":{"Status":"running"}}` {
		t.Fatalf("Inspect = %s, %v", inspect, err)
	}
	stats, err := service.Stats(t.Context(), sessionID)
	if err != nil || stats != "{\"CPUPerc\":\"0.5%\"}\n" {
		t.Fatalf("Stats = %q, %v", stats, err)
	}
	err = service.Action(t.Context(), docker.ActionRequest{
		SessionID: sessionID, AssetID: "device/d-1", Source: "user",
		Options: docker.ActionOptions{Container: "web", Action: docker.ActionRestart},
	})
	if err != nil {
		t.Fatalf("Action = %v", err)
	}
	pulled, err := service.ImagePull(t.Context(), docker.ImagePullRequest{SessionID: sessionID, Reference: "nginx:latest"})
	if err != nil || pulled != "pulled\n" {
		t.Fatalf("ImagePull = %q, %v", pulled, err)
	}
	executed, err := service.Exec(t.Context(), docker.ExecRequest{SessionID: sessionID, Container: "web", Command: "echo hi"})
	if err != nil || executed.Output != "hi\n" || executed.ExitCode != 0 {
		t.Fatalf("Exec = %+v, %v", executed, err)
	}
	wantCommands := []string{
		`docker 'ps' '-a' '--format' '{{json .}}'`,
		`docker 'images' '--format' '{{json .}}'`,
		`docker 'inspect' 'web'`,
		`docker 'stats' '--no-stream' '--format' '{{json .}}'`,
		`docker 'restart' 'web'`,
		`docker 'pull' 'nginx:latest'`,
		`docker 'exec' 'web' 'sh' '-c' 'echo hi'`,
	}
	if !slices.Equal(transport.commands, wantCommands) {
		t.Fatalf("commands = %q, want %q", transport.commands, wantCommands)
	}
	if transport.dialed {
		t.Fatal("device transport DialContext called: SDK backend must stay unsupported for device sessions")
	}
}

func TestDockerDeviceExecUnsupported(t *testing.T) {
	transport := &dockerDeviceTransport{execErr: base.ErrUnsupported}
	service, sessionID := newDockerDeviceTestService(t, transport)
	if _, err := service.PS(t.Context(), sessionID); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("PS error = %v, want unsupported", err)
	}
	err := service.Action(t.Context(), docker.ActionRequest{
		SessionID: sessionID, AssetID: "device/d-1", Source: "user",
		Options: docker.ActionOptions{Container: "web", Action: docker.ActionRestart},
	})
	if !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("Action error = %v, want unsupported", err)
	}
	if transport.dialed {
		t.Fatal("device transport DialContext called: SDK backend must stay unsupported for device sessions")
	}
}

func TestProductionDockerProviderRejectsForeignContextBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*docker.Service, context.Context, string) error
	}{
		{name: "query", call: func(service *docker.Service, ctx context.Context, sessionID string) error {
			_, err := service.PS(ctx, sessionID)
			return err
		}},
		{name: "mutation", call: func(service *docker.Service, ctx context.Context, sessionID string) error {
			return service.Action(ctx, docker.ActionRequest{
				SessionID: sessionID, Source: "user",
				Options: docker.ActionOptions{Container: "web", Action: docker.ActionRestart},
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &dockerShellTransport{kind: "ssh"}
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return transport, nil
			})
			manager := session.NewManager(session.Config{Connector: connector, OpenAccess: true})
			database, err := store.OpenInMemory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			service := newProductionDockerService(manager, database)
			t.Cleanup(func() {
				_ = service.Close()
				_ = manager.Close()
				_ = database.Close()
			})
			owner := ipc.WithUserID(t.Context(), "owner")
			connected, err := manager.Connect(owner, session.Asset{ID: "owner-asset", Kind: session.KindSSH})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.call(service, owner, connected.ID); err != nil {
				t.Fatalf("same-account Docker call = %v", err)
			}
			transport.commands = nil
			foreign := ipc.WithUserID(t.Context(), "foreign")
			if err := test.call(service, foreign, connected.ID); !errors.Is(err, session.ErrForbidden) {
				t.Errorf("foreign Docker call error = %v, want ErrForbidden", err)
			}
			if len(transport.commands) != 0 {
				t.Fatalf("foreign Docker call reached command backend: %q", transport.commands)
			}
		})
	}
}
