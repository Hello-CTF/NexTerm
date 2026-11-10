package session

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestClassifyShellProgram(t *testing.T) {
	for _, test := range []struct {
		program string
		want    Shell
	}{
		{`C:\Windows\System32\cmd.exe`, ShellCmd},
		{"CMD.EXE", ShellCmd},
		{`C:\Program Files\PowerShell\7\pwsh.exe`, ShellPowerShell},
		{"powershell", ShellPowerShell},
		{"/bin/bash", ShellPOSIX},
		{`C:\Program Files\Git\bin\bash.exe`, ShellPOSIX},
		{"/usr/bin/fish", ShellUnknown},
		{"", ShellUnknown},
	} {
		if got := ClassifyShellProgram(test.program); got != test.want {
			t.Errorf("ClassifyShellProgram(%q) = %q, want %q", test.program, got, test.want)
		}
	}
}

type detectingTestTransport struct {
	*fakeTransport
	detected atomic.Value
}

func (t *detectingTestTransport) DetectedShell() string {
	value, _ := t.detected.Load().(string)
	return value
}

func openShellTestSession(t *testing.T, asset Asset, transport base.Transport) (*Manager, *Session) {
	t.Helper()
	manager := NewManager(Config{
		Connector: ConnectorFunc(func(context.Context, Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		Terminals: newFakeTerminalFactory(),
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), asset)
	if err != nil {
		t.Fatal(err)
	}
	return manager, connected
}

func TestSSHTabShellFromAssetOption(t *testing.T) {
	transport := &detectingTestTransport{fakeTransport: newFakeTransport(KindSSH, 1)}
	transport.detected.Store("powershell.exe")
	asset := Asset{ID: "win", Kind: KindSSH, Options: map[string]any{"shell": `C:\Windows\System32\cmd.exe`}}
	manager, connected := openShellTestSession(t, asset, transport)
	if got := openTestTab(t, manager, connected, "client-a", "shell-a").Info().Shell; got != ShellCmd {
		t.Fatalf("shell = %q, want cmd from the asset option", got)
	}
}

func TestSSHTabShellLearnsProbedDefaultShell(t *testing.T) {
	transport := &detectingTestTransport{fakeTransport: newFakeTransport(KindSSH, 1)}
	manager, connected := openShellTestSession(t, Asset{ID: "win", Kind: KindSSH}, transport)
	tab := openTestTab(t, manager, connected, "client-a", "shell-a")
	if got := tab.Info().Shell; got != ShellUnknown {
		t.Fatalf("shell before any probe = %q, want unknown", got)
	}
	transport.detected.Store("cmd.exe")
	if got := tab.Info().Shell; got != ShellCmd {
		t.Fatalf("shell after the probe = %q, want cmd", got)
	}
	transport.detected.Store("powershell.exe")
	if got := tab.Info().Shell; got != ShellCmd {
		t.Fatalf("shell changed after it was learned: %q", got)
	}
	opened, err := manager.OpenTab(context.Background(), OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "shell-b", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Shell != ShellPowerShell {
		t.Fatalf("new tab shell = %q, want powershell", opened.Shell)
	}
}
