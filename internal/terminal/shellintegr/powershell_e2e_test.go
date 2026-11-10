package shellintegr

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/pty"
)

// TestWrappedPowerShellReportsCWDAndCommands runs wrapped PowerShell on a real PTY (ConPTY on Windows), changes
// into a directory whose name needs URL escaping, runs a native command that exits 3 and checks the recorded
// stream: the cwd tracker recovers the directory from OSC 7, the command is marked with OSC 133 B and finishes
// with D;3, and the user's prompt output still follows the prompt-start mark.
func TestWrappedPowerShellReportsCWDAndCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real shells")
	}
	for _, name := range []string{"pwsh", "powershell"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s not installed", name)
			}
			launch, err := Wrap(ShellPowerShell, path)
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()

			start := t.TempDir()
			target := filepath.Join(start, "sub dir#1")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			session, err := pty.Start(ctx, pty.Config{
				Path: launch.Path,
				Args: launch.Args,
				Dir:  start,
				Env:  append(os.Environ(), "TERM=xterm-256color"),
				Cols: 120,
				Rows: 30,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			enter := "\n"
			if runtime.GOOS == "windows" {
				enter = "\r"
			}
			script := "Set-Location -LiteralPath '" + strings.ReplaceAll(target, "'", "''") + "'" + enter +
				"& (Get-Process -Id $PID).Path -NoLogo -NoProfile -Command 'exit 3'" + enter +
				"exit" + enter
			if _, err := session.Write([]byte(script)); err != nil {
				t.Fatal(err)
			}
			output, err := io.ReadAll(session)
			if err != nil && len(output) == 0 {
				t.Fatal(err)
			}

			tracker := NewTracker()
			commands := NewCommandTracker()
			var letters []string
			for rest := output; len(rest) > 0; {
				chunk := rest[:min(11, len(rest))]
				rest = rest[len(chunk):]
				tracker.Observe(chunk)
				reports, _, _ := commands.ObserveReports(chunk)
				for _, report := range reports {
					letter := string(report.Letter)
					if report.HasExitCode {
						letter += ";" + strconv.Itoa(report.ExitCode)
					}
					letters = append(letters, letter)
				}
			}
			if got := filepath.Clean(filepath.FromSlash(tracker.CWD())); !strings.EqualFold(got, filepath.Clean(target)) {
				t.Fatalf("CWD() = %q; want %q\noutput:\n%q", got, target, output)
			}
			sequence := strings.Join(letters, " ")
			if !strings.Contains(sequence, "B C D;3 A") {
				t.Fatalf("OSC 133 reports = %q; want the failing command marked B C and finished with D;3\noutput:\n%q", sequence, output)
			}
			if mark := bytes.Index(output, []byte("\x1b]133;A")); mark < 0 || !bytes.Contains(output[mark:], []byte("PS ")) {
				t.Fatalf("the default prompt text does not follow the prompt mark\noutput:\n%q", output)
			}
		})
	}
}
