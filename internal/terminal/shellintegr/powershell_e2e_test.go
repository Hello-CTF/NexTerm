package shellintegr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

			// A terminal sends CR for Enter; PSReadLine inserts a literal newline for LF.
			const enter = "\r"
			script := "Set-Location -LiteralPath '" + strings.ReplaceAll(target, "'", "''") + "'" + enter +
				"& (Get-Process -Id $PID).Path -NoLogo -NoProfile -Command 'exit 3'" + enter +
				"exit" + enter
			output, err := driveShell(ctx, session, []byte(script))
			if err != nil {
				t.Fatalf("%v\noutput:\n%q", err, output)
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
			// The shell reports the long form; t.TempDir() may hold 8.3 short names on Windows.
			target = filepath.Clean(longPath(t, target))
			if got := filepath.Clean(filepath.FromSlash(tracker.CWD())); !strings.EqualFold(got, target) {
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

// driveShell plays the terminal for a shell on a PTY. PSReadLine asks for the cursor position (DSR, ESC[6n) and
// stalls until it is answered, and a script typed before the first prompt is echoed instead of run, so the script is
// written only after the first OSC 133;A prompt mark. One goroutine owns every read and write; the output is handed
// back over a channel when the shell exits or ctx ends.
func driveShell(ctx context.Context, session io.ReadWriteCloser, script []byte) ([]byte, error) {
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var output []byte
		var failure error
		answered, sent := 0, false
		chunk := make([]byte, 4096)
		for {
			n, err := session.Read(chunk)
			output = append(output, chunk[:n]...)
			if n > 0 {
				requests := bytes.Count(output, []byte("\x1b[6n"))
				for ; answered < requests; answered++ {
					if _, werr := session.Write([]byte("\x1b[1;1R")); werr != nil {
						failure = werr
					}
				}
				if !sent && bytes.Contains(output, []byte("\x1b]133;A")) {
					sent = true
					if _, werr := session.Write(script); werr != nil {
						failure = werr
					}
				}
			}
			if err != nil || failure != nil {
				done <- result{output, failure}
				return
			}
		}
	}()
	select {
	case r := <-done:
		return r.output, r.err
	case <-ctx.Done():
		session.Close()
		select {
		case r := <-done:
			return r.output, fmt.Errorf("shell did not finish: %w", ctx.Err())
		case <-time.After(5 * time.Second):
			return nil, fmt.Errorf("shell did not finish: %w", ctx.Err())
		}
	}
}
