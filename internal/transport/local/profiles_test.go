package local

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestWindowsShellProfiles(t *testing.T) {
	files := map[string]bool{
		`C:\Program Files\PowerShell\7\pwsh.exe`:                    true,
		`C:\Program Files\PowerShell\7-preview\pwsh.exe`:            true,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: true,
		`C:\Windows\system32\cmd.exe`:                               true,
		`C:\Windows\System32\wsl.exe`:                               true,
		`D:\Tools\Git\bin\bash.exe`:                                 true,
		`C:\Program Files\Git\bin\bash.exe`:                         true,
	}
	profiles := windowsShellProfiles(windowsShellSources{
		LookPath:     func(string) (string, error) { return "", errors.New("not found") },
		Exists:       func(path string) bool { return files[path] },
		SystemRoot:   `C:\Windows\`,
		ComSpec:      `C:\Windows\system32\cmd.exe`,
		ProgramFiles: []string{`C:\Program Files`},
		WSL: []WSLDistribution{
			{Name: "docker-desktop", State: 1},
			{Name: "Debian", State: 1},
			{Name: "Installing", State: 3},
			{Name: "Ubuntu-24.04", State: 1, Default: true},
			{Name: "", State: 1},
		},
		GitInstallPaths: []string{`D:\Missing`, `D:\Tools\Git\`},
	})
	want := []ShellProfile{
		{ID: "pwsh", Name: "PowerShell", Path: `C:\Program Files\PowerShell\7\pwsh.exe`},
		{ID: "pwsh-preview", Name: "PowerShell Preview", Path: `C:\Program Files\PowerShell\7-preview\pwsh.exe`},
		{ID: "powershell", Name: "Windows PowerShell", Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`},
		{ID: "cmd", Name: "Command Prompt", Path: `C:\Windows\system32\cmd.exe`},
		{ID: "wsl:Ubuntu-24.04", Name: "WSL: Ubuntu-24.04", Path: `C:\Windows\System32\wsl.exe`, Args: []string{"-d", "Ubuntu-24.04"}},
		{ID: "wsl:Debian", Name: "WSL: Debian", Path: `C:\Windows\System32\wsl.exe`, Args: []string{"-d", "Debian"}},
		{ID: "git-bash", Name: "Git Bash", Path: `D:\Tools\Git\bin\bash.exe`, Args: []string{"--login", "-i"}},
	}
	if !reflect.DeepEqual(profiles, want) {
		t.Fatalf("profiles =\n%#v\nwant\n%#v", profiles, want)
	}
}

func TestWindowsShellProfilesPrefersPwshOnPath(t *testing.T) {
	profiles := windowsShellProfiles(windowsShellSources{
		LookPath: func(name string) (string, error) {
			if name == "pwsh.exe" {
				return `C:\Users\me\AppData\Local\Microsoft\WindowsApps\pwsh.exe`, nil
			}
			return "", errors.New("not found")
		},
		Exists:       func(path string) bool { return strings.HasSuffix(path, `\7\pwsh.exe`) },
		ProgramFiles: []string{`C:\Program Files`},
	})
	if len(profiles) != 1 || profiles[0].Path != `C:\Users\me\AppData\Local\Microsoft\WindowsApps\pwsh.exe` {
		t.Fatalf("profiles = %#v, want only the pwsh found on PATH", profiles)
	}
}

func TestWindowsShellProfilesWithoutWSLExecutable(t *testing.T) {
	profiles := windowsShellProfiles(windowsShellSources{
		Exists:     func(string) bool { return false },
		SystemRoot: `C:\Windows`,
		WSL:        []WSLDistribution{{Name: "Ubuntu", State: 1, Default: true}},
	})
	if len(profiles) != 0 {
		t.Fatalf("profiles = %#v, want none when wsl.exe and the shells are missing", profiles)
	}
}

func TestParseEtcShells(t *testing.T) {
	content := `# /etc/shells: valid login shells
/bin/sh
/bin/bash
/usr/bin/bash   # merged /usr alias
/usr/bin/zsh
/usr/sbin/nologin
/bin/false
relative/fish
/opt/missing/fish

/usr/bin/with space
`
	canonical := map[string]string{"/bin/bash": "/usr/bin/bash", "/bin/sh": "/usr/bin/dash"}
	profiles := parseEtcShells(content, func(path string) bool { return path != "/opt/missing/fish" }, func(path string) string {
		if resolved, ok := canonical[path]; ok {
			return resolved
		}
		return path
	})
	want := []ShellProfile{
		{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"},
		{ID: "/bin/bash", Name: "bash", Path: "/bin/bash"},
		{ID: "/usr/bin/zsh", Name: "zsh", Path: "/usr/bin/zsh"},
	}
	if !reflect.DeepEqual(profiles, want) {
		t.Fatalf("profiles = %#v, want %#v", profiles, want)
	}
}
