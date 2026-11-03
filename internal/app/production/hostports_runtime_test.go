package production

import (
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hostports"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

func TestHostPortsShellSelection(t *testing.T) {
	cases := []struct {
		name  string
		asset session.Asset
		goos  string
		want  hostports.Shell
	}{
		{"winrm", session.Asset{Kind: session.KindWinRM}, "linux", hostports.ShellPowerShell},
		{"local linux", session.Asset{Kind: session.KindLocal}, "linux", hostports.ShellPOSIX},
		{"local windows", session.Asset{Kind: session.KindLocal}, "windows", hostports.ShellPowerShell},
		{"device defaults to posix", session.Asset{Kind: session.KindDevice}, "windows", hostports.ShellPOSIX},
		{"ssh unknown", session.Asset{Kind: session.KindSSH}, "linux", hostports.ShellUnknown},
		{"ssh pwsh", session.Asset{Kind: session.KindSSH, Options: map[string]any{"shell": `C:\Program Files\PowerShell\7\pwsh.exe`}}, "linux", hostports.ShellPowerShell},
		{"ssh bash", session.Asset{Kind: session.KindSSH, Options: map[string]any{"shell": "/bin/bash"}}, "linux", hostports.ShellUnknown},
	}
	for _, tc := range cases {
		if got := hostPortsShellForGOOS(tc.asset, tc.goos); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestTerminalOutputAge(t *testing.T) {
	tabs := []session.TabInfo{
		{SessionID: "a", LastOutputAt: 10, LastOutputMSAgo: 5000},
		{SessionID: "a", LastOutputAt: 20, LastOutputMSAgo: 800},
		{SessionID: "a"},
		{SessionID: "b", LastOutputAt: 30, LastOutputMSAgo: 1},
	}
	if age, ok := terminalOutputAge(tabs, "a"); !ok || age != 800*time.Millisecond {
		t.Fatalf("age = %v %v", age, ok)
	}
	if _, ok := terminalOutputAge(tabs, "c"); ok {
		t.Fatal("unknown session reported an age")
	}
	if _, ok := terminalOutputAge([]session.TabInfo{{SessionID: "a"}}, "a"); ok {
		t.Fatal("tab without output reported an age")
	}
}
