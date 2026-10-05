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

	"github.com/ProbiusOfficial/NexTerm/internal/pty"
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
