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
			if !strings.Contains(strings.Join(launch.command, " "), "rcfile") &&
				!strings.Contains(strings.Join(launch.command, " "), "init-command") &&
				!strings.Contains(strings.Join(launch.command, " "), "-i") {
				t.Fatalf("wrapped command %v does not look interactive", launch.command)
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
