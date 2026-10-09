package shellintegr

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/pty"
)

// TestBashWrapperPreservesUserPromptCommand runs wrapped bash on a PTY with a
// user .bashrc that sets PROMPT_COMMAND and asserts both the user's command
// and the OSC 7 report execute, ours first. On bash >= 5.1 this covers the
// array branch; on older bash the scalar branch.
func TestBashWrapperPreservesUserPromptCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	launch, err := Wrap(ShellBash, path)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()

	home := t.TempDir()
	bashrc := "PROMPT_COMMAND='printf USERPROMPT-RAN'\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(bashrc), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := pty.Start(ctx, pty.Config{
		Path: launch.Path,
		Args: launch.Args,
		Dir:  t.TempDir(),
		Env: []string{
			"HOME=" + home,
			"PATH=" + os.Getenv("PATH"),
			"TERM=xterm-256color",
		},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}

	osc := bytes.Index(output, []byte("\x1b]7;file://"))
	user := bytes.Index(output, []byte("USERPROMPT-RAN"))
	if osc < 0 || user < 0 {
		t.Fatalf("missing osc7 (at %d) or user command (at %d) in output:\n%s", osc, user, output)
	}
	if osc > user {
		t.Fatal("user PROMPT_COMMAND ran before the cwd report; want prepend order")
	}
}

// TestWrappedShellsReportCWD launches each supported shell on a real PTY with
// the wrapper injected, changes into a directory whose name needs escaping,
// and checks the tracker recovers it from the byte stream the recorder would
// store. Shells missing from PATH are skipped.
func TestWrappedShellsReportCWD(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real shells")
	}
	for _, shell := range []Shell{ShellBash, ShellZsh, ShellFish} {
		t.Run(string(shell), func(t *testing.T) {
			path, err := exec.LookPath(string(shell))
			if err != nil {
				t.Skipf("%s not installed", shell)
			}
			launch, err := Wrap(shell, path)
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()

			home := t.TempDir()
			start := t.TempDir()
			target := filepath.Join(start, "sub dir#1")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}

			env := []string{
				"HOME=" + home,
				"PATH=" + os.Getenv("PATH"),
				"TERM=xterm-256color",
			}
			if ld := os.Getenv("LD_LIBRARY_PATH"); ld != "" {
				env = append(env, "LD_LIBRARY_PATH="+ld)
			}
			env = append(env, launch.Env...)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			session, err := pty.Start(ctx, pty.Config{
				Path: launch.Path,
				Args: launch.Args,
				Dir:  start,
				Env:  env,
				Cols: 80,
				Rows: 24,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			if _, err := session.Write([]byte("cd '" + target + "'\nexit\n")); err != nil {
				t.Fatal(err)
			}
			output, err := io.ReadAll(session)
			if err != nil {
				t.Fatal(err)
			}
			full := output

			tracker := NewTracker()
			var recorded bytes.Buffer
			for len(output) > 0 {
				n := min(11, len(output))
				chunk := output[:n]
				tracker.Observe(chunk)
				recorded.Write(chunk)
				output = output[n:]
			}
			if !bytes.Equal(recorded.Bytes(), full) {
				t.Fatal("recorded stream differs from shell output")
			}
			if !bytes.Contains(full, []byte("\x1b]7;file://")) {
				t.Fatalf("wrapper emitted no OSC 7 report, output:\n%s", full)
			}
			if got := tracker.CWD(); got != target {
				t.Fatalf("CWD() = %q; want %q", got, target)
			}
		})
	}
}

// TestWrappedBashEmitsOSC133 runs wrapped bash on a PTY, executes a command
// that fails, and asserts the stream carries a full OSC 133 lifecycle - prompt
// start (A), command start (B), and command finished with the real exit code
// (D;1) - in order, alongside the OSC 7 cwd report, and that the passive
// CommandTracker recovers the exit code from the recorded bytes.
func TestWrappedBashEmitsOSC133(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	launch, err := Wrap(ShellBash, path)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()

	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := pty.Start(ctx, pty.Config{
		Path: launch.Path,
		Args: launch.Args,
		Dir:  t.TempDir(),
		Env: []string{
			"HOME=" + home,
			"PATH=" + os.Getenv("PATH"),
			"TERM=xterm-256color",
		},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("false\nexit\n")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}

	aSeq := []byte("\x1b]133;A\x1b\\")
	bSeq := []byte("\x1b]133;B\x1b\\")
	dSeq := []byte("\x1b]133;D;1\x1b\\")
	if !bytes.Contains(output, []byte("\x1b]7;file://")) {
		t.Fatalf("wrapped bash dropped the OSC 7 report:\n%s", output)
	}
	for _, want := range [][]byte{aSeq, bSeq, dSeq} {
		if !bytes.Contains(output, want) {
			t.Fatalf("wrapped bash output missing %q:\n%s", want, output)
		}
	}
	a := bytes.Index(output, aSeq)
	b := bytes.Index(output, bSeq)
	d := bytes.Index(output, dSeq)
	if !(a >= 0 && a < b && b < d) {
		t.Fatalf("OSC 133 lifecycle out of order: A=%d B=%d D=%d\n%s", a, b, d, output)
	}

	tracker := NewCommandTracker()
	full := output
	for len(output) > 0 {
		n := min(5, len(output))
		chunk := output[:n]
		tracker.Observe(chunk)
		output = output[n:]
	}
	state := tracker.State()
	if !state.HasLastExitCode || state.LastExitCode != 1 {
		t.Fatalf("CommandTracker exit = %d (present=%v); want 1, true; stream:\n%s", state.LastExitCode, state.HasLastExitCode, full)
	}
	if state.Sequence < 1 {
		t.Fatalf("CommandTracker.Sequence = %d; want >= 1", state.Sequence)
	}
}

// TestBashWrapperPreservesUserDebugTrap installs a user DEBUG trap alongside a
// user PROMPT_COMMAND and asserts both still run under the wrapper, so the
// OSC 133 hooks do not clobber existing rc hooks.
func TestBashWrapperPreservesUserDebugTrap(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	launch, err := Wrap(ShellBash, path)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()

	home := t.TempDir()
	bashrc := "PROMPT_COMMAND='printf USERPROMPT-RAN'\ntrap 'printf USERDEBUG-RAN' DEBUG\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(bashrc), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := pty.Start(ctx, pty.Config{
		Path: launch.Path,
		Args: launch.Args,
		Dir:  t.TempDir(),
		Env: []string{
			"HOME=" + home,
			"PATH=" + os.Getenv("PATH"),
			"TERM=xterm-256color",
		},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte("USERPROMPT-RAN")) {
		t.Fatalf("user PROMPT_COMMAND did not run:\n%s", output)
	}
	if !bytes.Contains(output, []byte("USERDEBUG-RAN")) {
		t.Fatalf("user DEBUG trap did not run:\n%s", output)
	}
	if !bytes.Contains(output, []byte("\x1b]133;A\x1b\\")) {
		t.Fatalf("wrapper stopped emitting OSC 133:\n%s", output)
	}
}
