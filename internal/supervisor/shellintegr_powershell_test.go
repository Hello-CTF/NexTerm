package supervisor

import (
	"reflect"
	"testing"
)

func TestWrapShellIntegrationPowerShell(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		// prefix is the wrapped command up to the wrapper's -NoExit -Command pair; nil means not wrapped.
		prefix []string
	}{
		{"windows default", []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoLogo"}, []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoLogo"}},
		{"windows powershell", []string{"powershell.exe"}, []string{"powershell.exe", "-NoLogo"}},
		{"unix login keeps order", []string{"/usr/bin/pwsh", "-Login", "-NoProfile"}, []string{"/usr/bin/pwsh", "-Login", "-NoProfile", "-NoLogo"}},
		{"custom command", []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NoExit", "-Command", "Write-Host hi"}, nil},
		{"script file", []string{"pwsh", "-File", "setup.ps1"}, nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			launch, wrapped := wrapShellIntegration(test.command, []string{"TERM=xterm"})
			if wrapped != (test.prefix != nil) {
				t.Fatalf("wrapped = %v; want %v", wrapped, test.prefix != nil)
			}
			if !wrapped {
				return
			}
			defer launch.cleanup()
			if len(launch.command) != len(test.prefix)+3 {
				t.Fatalf("wrapped command = %q", launch.command)
			}
			if got := launch.command[:len(test.prefix)]; !reflect.DeepEqual(got, test.prefix) {
				t.Fatalf("wrapped command starts %q; want %q", got, test.prefix)
			}
			if rest := launch.command[len(test.prefix):]; rest[0] != "-NoExit" || rest[1] != "-Command" {
				t.Fatalf("wrapped command = %q; want the hooks loaded with -NoExit -Command", launch.command)
			}
			if !reflect.DeepEqual(launch.env, []string{"TERM=xterm"}) {
				t.Fatalf("wrapped env = %q", launch.env)
			}
		})
	}
}
