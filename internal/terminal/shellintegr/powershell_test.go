package shellintegr

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPowerShellArgsQuotePath(t *testing.T) {
	args := PowerShellArgs(`C:\Users\O'Brien\AppData\Local\Temp\nexterm-shellintegr-1\nexterm.ps1`)
	want := []string{"-NoLogo", "-NoExit", "-Command",
		`try { . ([scriptblock]::Create([System.IO.File]::ReadAllText('C:\Users\O''Brien\AppData\Local\Temp\nexterm-shellintegr-1\nexterm.ps1'))) } catch { }`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("PowerShellArgs = %q, want %q", args, want)
	}
}

func TestPowerShellArgsQuoteTypographicApostrophe(t *testing.T) {
	args := PowerShellArgs("C:\\Users\\O\u2019Brien\\nexterm.ps1")
	want := "ReadAllText('C:\\Users\\O\u2019\u2019Brien\\nexterm.ps1')"
	if !strings.Contains(args[3], want) {
		t.Fatalf("command = %q; want it to contain %q", args[3], want)
	}
}

func TestWrapPowerShell(t *testing.T) {
	path := `C:\Program Files\PowerShell\7\pwsh.exe`
	launch, err := Wrap(ShellPowerShell, path)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	if launch.Path != path || len(launch.Env) != 0 {
		t.Fatalf("launch = %+v; want the executable kept and no environment", launch)
	}
	if len(launch.Args) != 4 || launch.Args[1] != "-NoExit" || launch.Args[2] != "-Command" {
		t.Fatalf("Args = %q", launch.Args)
	}
	for _, arg := range launch.Args {
		if strings.EqualFold(arg, "-NoProfile") || strings.EqualFold(arg, "-ExecutionPolicy") {
			t.Fatalf("Args = %q; the user's profiles and execution policy must stay in effect", launch.Args)
		}
	}
	command := launch.Args[3]
	start := strings.Index(command, "ReadAllText('") + len("ReadAllText('")
	end := strings.Index(command[start:], "')")
	if start < len("ReadAllText('") || end < 0 {
		t.Fatalf("command = %q; want the hooks file read by path", command)
	}
	file := strings.ReplaceAll(command[start:start+end], "''", "'")
	if filepath.Base(file) != PowerShellIntegrationFile {
		t.Fatalf("hooks file = %q", file)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"$function:prompt",
		"function global:prompt",
		"$global:?",
		"$global:LASTEXITCODE",
		"]133;D;$code",
		"]133;A",
		"]7;file://",
		"EscapeDataString",
		"function global:PSConsoleHostReadLine",
		"]133;B",
		"]133;C",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("PowerShell wrapper missing %q", want)
		}
	}
}
