//go:build unix

package supervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

func TestWrapShellIntegrationDecision(t *testing.T) {
	cases := []struct {
		name    string
		command []string
		wrapped bool
	}{
		{"default bash login", []string{"/bin/bash", "-l"}, true},
		{"default zsh", []string{"/bin/zsh"}, true},
		{"fish interactive", []string{"fish", "-i"}, true},
		{"bash login long flag", []string{"bash", "--login"}, true},
		{"unsupported shell", []string{"/bin/sh", "-l"}, false},
		{"custom command", []string{"/bin/bash", "-c", "echo hi"}, false},
		{"empty command", nil, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			launch, wrapped := wrapShellIntegration(test.command, []string{"TERM=xterm"})
			if wrapped != test.wrapped {
				t.Fatalf("wrapped = %v; want %v", wrapped, test.wrapped)
			}
			if !wrapped {
				return
			}
			if launch.cleanup == nil {
				t.Fatal("wrapped launch has no cleanup")
			}
			defer launch.cleanup()
			if len(launch.command) == 0 || launch.command[0] != test.command[0] {
				t.Fatalf("wrapped command = %v; want shell %q kept", launch.command, test.command[0])
			}
			found := false
			for _, entry := range launch.env {
				if entry == "TERM=xterm" {
					found = true
				}
			}
			if !found {
				t.Fatalf("wrapped env %v lost the caller environment", launch.env)
			}
		})
	}
}

func TestWrapShellIntegrationPreservesLoginMode(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	launch, wrapped := wrapShellIntegration([]string{bash, "-l"}, nil)
	if !wrapped {
		t.Fatal("bash login launch was not wrapped")
	}
	defer launch.cleanup()
	if len(launch.command) != 4 || launch.command[1] != "--rcfile" || launch.command[3] != "-i" {
		t.Fatalf("bash login command = %v; want [--rcfile <login-rc> -i]", launch.command)
	}
	composite, err := os.ReadFile(launch.command[2])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(composite), "/etc/profile") ||
		!strings.Contains(string(composite), ".bash_profile") ||
		!strings.Contains(string(composite), ".bash_login") ||
		!strings.Contains(string(composite), ".profile") {
		t.Fatalf("composite rcfile misses the login chain:\n%s", composite)
	}
	wrapperPath := ""
	for _, line := range strings.Split(string(composite), "\n") {
		if strings.HasPrefix(line, "source '") {
			wrapperPath = strings.TrimSuffix(strings.TrimPrefix(line, "source '"), "'")
		}
	}
	if wrapperPath == "" {
		t.Fatalf("composite rcfile chains into no wrapper:\n%s", composite)
	}
	wrapper, err := os.ReadFile(wrapperPath)
	if err != nil {
		t.Fatalf("composite sources a missing wrapper: %v", err)
	}
	if !strings.Contains(string(wrapper), "__nexterm_osc7") {
		t.Fatal("composite does not chain into the shell integration wrapper")
	}
	if filepath.Dir(launch.command[2]) != filepath.Dir(wrapperPath) {
		t.Fatal("composite rcfile lives outside the wrapper directory")
	}

	zshLaunch, wrapped := wrapShellIntegration([]string{"zsh", "-l"}, nil)
	if !wrapped {
		t.Fatal("zsh login launch was not wrapped")
	}
	defer zshLaunch.cleanup()
	if len(zshLaunch.command) != 3 || zshLaunch.command[1] != "-l" || zshLaunch.command[2] != "-i" {
		t.Fatalf("zsh login command = %v; want [-l -i]", zshLaunch.command)
	}

	fishLaunch, wrapped := wrapShellIntegration([]string{"fish", "--login"}, nil)
	if !wrapped {
		t.Fatal("fish login launch was not wrapped")
	}
	defer fishLaunch.cleanup()
	if len(fishLaunch.command) < 4 || fishLaunch.command[1] != "-l" || fishLaunch.command[2] != "-i" || fishLaunch.command[3] != "--init-command" {
		t.Fatalf("fish login command = %v; want [-l -i --init-command ...]", fishLaunch.command)
	}
}

func TestWrapShellIntegrationZshCleanup(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	launch, wrapped := wrapShellIntegration([]string{"zsh", "-l"}, nil)
	if !wrapped {
		t.Fatal("zsh launch was not wrapped")
	}
	dir := ""
	for _, entry := range launch.env {
		if value, found := strings.CutPrefix(entry, "ZDOTDIR="); found {
			dir = value
		}
	}
	if dir == "" {
		t.Fatalf("wrapped zsh env %v has no ZDOTDIR", launch.env)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("wrapper dir missing before cleanup: %v", err)
	}
	launch.cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("wrapper dir still present after cleanup: %v", err)
	}
}

func TestSupervisorWrappedShellReportsCWD(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	supervisor := testSupervisor(t)
	home := t.TempDir()
	start := t.TempDir()
	target := filepath.Join(start, "sub dir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{bash, "-l"},
		Env:     []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.entry.Command; len(got) != 2 || got[0] != bash || got[1] != "-l" {
		t.Fatalf("registry command = %v; want the unwrapped launch recorded", got)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Detach()
	if _, err := session.Write([]byte("cd '" + target + "'\nexit\n")); err != nil {
		t.Fatal(err)
	}
	output := readUntil(t, attachment, "sub%20dir")
	if !strings.Contains(string(output), "\x1b]7;file://") {
		t.Fatalf("no OSC 7 report in output %q", output)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := session.Wait(waitCtx); err != nil {
		t.Fatalf("session wait: %v", err)
	}
	recording, err := os.ReadFile(recordingPath(supervisor.StateDir(), session.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recording), "\x1b]7;file://") {
		t.Fatal("OSC 7 report missing from the recording replay stream")
	}
}

func TestSupervisorWrappedBashLoginReadsLoginChain(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	supervisor := testSupervisor(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("printf 'PROFILE-MARKER\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("printf 'BASHRC-MARKER\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{bash, "-l"},
		Env:     []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Detach()
	output := readUntil(t, attachment, "\x1b]7;file://")
	text := string(output)
	if !strings.Contains(text, "PROFILE-MARKER") {
		t.Fatalf("login startup file was not read; output %q", text)
	}
	if !strings.Contains(text, "BASHRC-MARKER") {
		t.Fatalf("interactive rcfile was not read; output %q", text)
	}
	if strings.Index(text, "PROFILE-MARKER") > strings.Index(text, "BASHRC-MARKER") {
		t.Fatalf("login chain ran after the interactive rcfile; output %q", text)
	}
	killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.killAndWait(killCtx, supervisor.commandTimeout); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorWrappedZshLoginReadsLoginFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	supervisor := testSupervisor(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".zprofile"), []byte("printf 'ZPROFILE-MARKER\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("printf 'ZSHRC-MARKER\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{zsh, "-l"},
		Env:     []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Detach()
	output := readUntil(t, attachment, "\x1b]7;file://")
	text := string(output)
	if !strings.Contains(text, "ZPROFILE-MARKER") {
		t.Fatalf("zsh login file was not read; output %q", text)
	}
	if !strings.Contains(text, "ZSHRC-MARKER") {
		t.Fatalf("zsh interactive file was not read; output %q", text)
	}
	killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.killAndWait(killCtx, supervisor.commandTimeout); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorWrappedFishLogin(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not installed")
	}
	supervisor := testSupervisor(t)
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "fish")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.fish"), []byte("printf 'FISH-CONFIG-MARKER\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{fish, "-l"},
		Env:     []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Detach()
	output := readUntil(t, attachment, "\x1b]7;file://")
	if !strings.Contains(string(output), "FISH-CONFIG-MARKER") {
		t.Fatalf("fish config was not read; output %q", output)
	}
	killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := session.killAndWait(killCtx, supervisor.commandTimeout); err != nil {
		t.Fatal(err)
	}
}

// TestSupervisorWrappedShellEmitsOSC133 launches a supervisor-backed wrapped
// bash, runs a command that fails, and asserts the live stream and the
// recording replay stream both carry the OSC 133 lifecycle (A, B, D;1) plus
// the OSC 7 report, and that a passive CommandTracker recovers the exit code
// from the recorded bytes. This proves the supervisor launch path emits OSC
// 133 without altering the recorded bytes.
func TestSupervisorWrappedShellEmitsOSC133(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	supervisor := testSupervisor(t)
	home := t.TempDir()
	session, err := supervisor.Create(context.Background(), CreateOptions{
		Command: []string{bash, "-l"},
		Env:     []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := supervisor.Attach(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Detach()
	if _, err := session.Write([]byte("false\nexit 0\n")); err != nil {
		t.Fatal(err)
	}
	output := readUntil(t, attachment, "\x1b]133;D;1\x1b\\")
	for _, want := range []string{"\x1b]133;A\x1b\\", "\x1b]133;B\x1b\\", "\x1b]133;D;1\x1b\\", "\x1b]7;file://"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("live output missing %q:\n%s", want, output)
		}
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := session.Wait(waitCtx); err != nil {
		t.Fatalf("session wait: %v", err)
	}
	recording, err := os.ReadFile(recordingPath(supervisor.StateDir(), session.ID()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b]133;A\x1b\\", "\x1b]133;B\x1b\\", "\x1b]133;D;1\x1b\\", "\x1b]7;file://"} {
		if !strings.Contains(string(recording), want) {
			t.Fatalf("recording replay stream missing %q", want)
		}
	}
	tracker := shellintegr.NewCommandTracker()
	tracker.Observe(recording)
	state := tracker.State()
	if !state.HasLastExitCode || state.LastExitCode != 1 {
		t.Fatalf("CommandTracker exit = %d (present=%v); want 1, true", state.LastExitCode, state.HasLastExitCode)
	}
}
