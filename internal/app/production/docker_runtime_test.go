package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/docker"
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
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &dockerShellTransport{kind: testCase.transportID}
			connector := session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
				return transport, nil
			})
			manager := session.NewManager(session.Config{Connector: connector})
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
