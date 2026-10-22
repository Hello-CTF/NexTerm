package ssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type discoveryRunner struct {
	mu       sync.Mutex
	commands []string
	run      func(string) (string, int, error)
}

func (r *discoveryRunner) Run(_ context.Context, command string, _ time.Duration) (string, int, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	return r.run(command)
}

func (r *discoveryRunner) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

func TestSupportedTargetsNine(t *testing.T) {
	targets := SupportedTargets()
	want := []string{
		"linux-amd64", "linux-arm64", "linux-armv7", "linux-riscv64", "linux-loong64",
		"darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64",
	}
	if len(targets) != len(want) {
		t.Fatalf("SupportedTargets returned %d targets, want %d", len(targets), len(want))
	}
	for index, target := range targets {
		t.Run(target.ID(), func(t *testing.T) {
			if got := target.ID(); got != want[index] {
				t.Fatalf("target ID = %q, want %q", got, want[index])
			}
			if target.GOOS == "windows" {
				target.DataRoot = `C:\Users\alice\AppData\Local`
			} else {
				target.DataRoot = "/home/alice/.local/share"
			}
			paths, err := target.Paths()
			if err != nil {
				t.Fatal(err)
			}
			digest := strings.Repeat("ab", 32)
			helper, err := paths.Helper(digest)
			if err != nil {
				t.Fatal(err)
			}
			if target.GOOS == "windows" {
				if !strings.HasSuffix(helper, `\nexterm-helper-`+digest+`.exe`) {
					t.Fatalf("Windows helper path = %q", helper)
				}
			} else if !strings.HasSuffix(helper, "/nexterm-helper-"+digest) {
				t.Fatalf("POSIX helper path = %q", helper)
			}
			staging, err := paths.staging(digest, "01020304")
			if err != nil {
				t.Fatal(err)
			}
			if target.GOOS == "windows" && !strings.HasSuffix(staging, ".partial.exe") {
				t.Fatalf("Windows staging path is not executable: %q", staging)
			} else if target.GOOS != "windows" && !strings.HasSuffix(staging, ".partial") {
				t.Fatalf("POSIX staging path = %q", staging)
			}
			source := UploadSource{GOOS: target.GOOS, GOARCH: target.GOARCH, GOARM: target.GOARM, Digest: digest}
			if err := source.ValidateForTarget(target); err != nil {
				t.Fatalf("matching source rejected: %v", err)
			}
		})
	}
}

func TestDiscoverPOSIXTargets(t *testing.T) {
	for _, test := range []struct {
		name    string
		goos    string
		machine string
		wantOS  string
		arch    string
		goarm   string
	}{
		{name: "Linux amd64", goos: "Linux", machine: "x86_64", wantOS: "linux", arch: "amd64"},
		{name: "Linux arm64", goos: "Linux", machine: "aarch64", wantOS: "linux", arch: "arm64"},
		{name: "Linux armv7", goos: "Linux", machine: "armv7l", wantOS: "linux", arch: "arm", goarm: "7"},
		{name: "Linux riscv64", goos: "Linux", machine: "riscv64", wantOS: "linux", arch: "riscv64"},
		{name: "Linux loong64", goos: "Linux", machine: "loongarch64", wantOS: "linux", arch: "loong64"},
		{name: "macOS amd64", goos: "Darwin", machine: "x86_64", wantOS: "darwin", arch: "amd64"},
		{name: "macOS arm64", goos: "Darwin", machine: "arm64", wantOS: "darwin", arch: "arm64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := "/home/alice/.local/share"
			if test.wantOS == "darwin" {
				root = "/Users/alice/Library/Application Support"
			}
			runner := &discoveryRunner{}
			runner.run = func(command string) (string, int, error) {
				if test.wantOS == "darwin" && !strings.Contains(command, "Library/Application Support") {
					t.Fatalf("macOS discovery does not provide its native data root: %q", command)
				}
				return fmt.Sprintf("%s\n%s\n%s\n501\n", root, test.goos, test.machine), 0, nil
			}
			target, err := DiscoverTarget(context.Background(), runner)
			if err != nil {
				t.Fatal(err)
			}
			if target.GOOS != test.wantOS || target.GOARCH != test.arch || target.GOARM != test.goarm || target.DataRoot != root || target.Identity != "501" {
				t.Fatalf("discovered target = %+v", target)
			}
			if len(runner.Calls()) != 1 {
				t.Fatalf("discovery unexpectedly tried another shell: %v", runner.Calls())
			}
		})
	}
}

func TestDiscoverWindowsOpenSSHTargets(t *testing.T) {
	for _, architecture := range []struct {
		reported string
		want     string
	}{{reported: "X64", want: "amd64"}, {reported: "Arm64", want: "arm64"}} {
		t.Run(architecture.want, func(t *testing.T) {
			runner := &discoveryRunner{}
			runner.run = func(command string) (string, int, error) {
				if !strings.HasPrefix(command, "powershell.exe ") {
					return "", 9009, nil
				}
				script := decodePowerShellScript(t, command)
				if strings.Contains(script, "uname") || strings.Contains(script, "setsid") {
					t.Fatalf("Windows discovery assumes POSIX tools: %s", script)
				}
				document := map[string]string{
					"data_root":    `C:\Users\alice\AppData\Local`,
					"architecture": architecture.reported,
					"identity":     "S-1-5-21-42",
				}
				raw, _ := json.Marshal(document)
				return string(raw) + "\n", 0, nil
			}
			target, err := DiscoverTarget(context.Background(), runner)
			if err != nil {
				t.Fatal(err)
			}
			if target.GOOS != "windows" || target.GOARCH != architecture.want || target.DataRoot != `C:\Users\alice\AppData\Local` || target.Identity != "S-1-5-21-42" {
				t.Fatalf("Windows target = %+v", target)
			}
			if len(runner.Calls()) != 2 {
				t.Fatalf("Windows discovery calls = %v", runner.Calls())
			}
		})
	}
}

func TestDiscoveryPreservesTransportFailureWithoutShellFallback(t *testing.T) {
	cause := errors.New("connection lost")
	runner := &discoveryRunner{run: func(string) (string, int, error) { return "", 0, cause }}
	_, err := DiscoverTarget(context.Background(), runner)
	if !errors.Is(err, cause) {
		t.Fatalf("DiscoverTarget error = %v, want original transport cause", err)
	}
	if len(runner.Calls()) != 1 {
		t.Fatalf("transport failure triggered another probe: %v", runner.Calls())
	}
}

func TestLaunchCommandsDoNotAssumeSetsidOnMacOSOrWindows(t *testing.T) {
	darwin := Target{GOOS: "darwin", GOARCH: "arm64"}
	command, err := darwin.LaunchCommand("/Users/alice/NexTerm/helper", "/Users/alice/Library/Application Support/nexterm/daemon")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "setsid") || !strings.Contains(command, "nohup") {
		t.Fatalf("macOS launch command = %q", command)
	}

	windows := Target{GOOS: "windows", GOARCH: "amd64"}
	command, err = windows.LaunchCommand(`C:\NexTerm\helper.exe`, `C:\Users\alice\AppData\Local\nexterm\daemon state`)
	if err != nil {
		t.Fatal(err)
	}
	script := decodePowerShellScript(t, command)
	if strings.Contains(command, "setsid") || strings.Contains(script, "uname") || !strings.Contains(script, "Start-Process") {
		t.Fatalf("Windows launch command = %q; script = %s", command, script)
	}
	for _, fragment := range []string{`C:\NexTerm\helper.exe`, `daemon state`, "helper.log", "helper.error.log"} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("Windows launch script lacks %q: %s", fragment, script)
		}
	}
}

func TestDarwinLaunchCommandHostShell(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS host-shell regression")
	}
	directory := t.TempDir()
	stateDir := filepath.Join(directory, "daemon state")
	helper := filepath.Join(directory, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := Target{GOOS: "darwin", GOARCH: runtime.GOARCH}
	command, err := target.LaunchCommand(helper, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.WaitDelay = time.Second
	if output, err := process.CombinedOutput(); err != nil {
		t.Fatalf("Darwin launch failed through host shell: %v: %s", err, output)
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("state directory mode = %o, want 700", got)
	}
}

func TestArmv7UploadRequiresExactGOARM(t *testing.T) {
	target := Target{GOOS: "linux", GOARCH: "arm", GOARM: "7"}
	source := UploadSource{GOOS: "linux", GOARCH: "arm", GOARM: "6", Digest: strings.Repeat("cd", 32)}
	if err := source.ValidateForTarget(target); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("GOARM=6 validation = %v, want unsupported", err)
	}
}
