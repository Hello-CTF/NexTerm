//go:build linux || darwin

package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// recordCorpusEnv, set to 1, makes TestRecordStateCorpus regenerate the
// recorded files of testdata/states from the scenarios below. It needs bash,
// python3 and vim. Recordings are sanitised: the host name, user name and
// temporary paths are replaced, and the shells start with an empty home.
const recordCorpusEnv = "NEXTERM_RECORD_STATE_CORPUS"

type corpusStep struct {
	// wait is the delay after the previous step, input what is then typed.
	wait  time.Duration
	input string
}

type corpusScenario struct {
	header corpusHeader
	// command overrides the interactive bash session.
	command []string
	// scripts are written to the working directory before the start.
	scripts map[string]string
	steps   []corpusStep
	// tail is how long to keep recording after the last step.
	tail time.Duration
}

func canonical(on bool) *bool { return &on }

// Stand-in programs. They draw generic prompt shapes and status lines; none
// of them imitates a particular product's wording.
const (
	scriptAsk = `import sys
answer = input("Overwrite settings.json? [y/N] ")
print("answered", answer)
`
	scriptMenu = `print("1) staging")
print("2) production")
print("3) cancel")
choice = input("Deploy target? ")
print("deploying to", choice)
`
	scriptPassword = `import getpass
getpass.getpass("Password: ")
print("ok")
`
	scriptSpinner = `import sys, time
frames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
for i in range(40):
    sys.stdout.write("\r" + frames[i % len(frames)] + " Thinking... (" + str(i // 10) + "s, esc to interrupt)")
    sys.stdout.flush()
    time.sleep(0.1)
sys.stdout.write("\r\x1b[KDone.\n")
`
	// scriptTUI is a raw-mode program that works with a spinner, then
	// draws a framed menu with a selection pointer and waits for a key;
	// with "--notify" it also sends an OSC 9 notification while waiting.
	scriptTUI = `import os, sys, termios, time, tty
notify = "--notify" in sys.argv
fd = sys.stdin.fileno()
saved = termios.tcgetattr(fd)
tty.setcbreak(fd)
attrs = termios.tcgetattr(fd)
attrs[3] &= ~termios.ECHO
termios.tcsetattr(fd, termios.TCSANOW, attrs)
out = sys.stdout
frames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
try:
    out.write("\x1b[?25l")
    for i in range(20):
        out.write("\r" + frames[i % len(frames)] + " Working on the change...")
        out.flush()
        time.sleep(0.1)
    out.write("\r\x1b[K")
    out.write("╭──────────────────────────────────────╮\r\n")
    out.write("│ Apply the edit to main.go?           │\r\n")
    out.write("│                                      │\r\n")
    out.write("│ ❯ 1. Yes                             │\r\n")
    out.write("│   2. Yes, and don't ask again        │\r\n")
    out.write("│   3. No, tell me what to change      │\r\n")
    out.write("╰──────────────────────────────────────╯\r\n")
    out.write("\x1b[?25h")
    if notify:
        out.write("\x1b]9;Permission needed\x07")
    out.flush()
    key = os.read(fd, 16)
    out.write("\x1b[7A\x1b[J")
    out.write("✓ Applied\r\n")
    out.flush()
finally:
    termios.tcsetattr(fd, termios.TCSANOW, saved)
`
)

func corpusScenarios() []corpusScenario {
	bash := func(name, origin, description string, expect []string, steps ...corpusStep) corpusScenario {
		return corpusScenario{
			header: corpusHeader{Version: 1, Name: name, Origin: origin, Description: description, Cols: 80, Rows: 24, ShellLeader: true, Expect: expect},
			steps:  steps,
			tail:   1500 * time.Millisecond,
		}
	}
	job := func(s corpusScenario, line bool) corpusScenario {
		s.header.Foreground.Job = true
		s.header.Foreground.Canonical = canonical(line)
		return s
	}
	scenarios := []corpusScenario{
		bash("shell-command-finish", "recorded: bash with the NexTerm integration",
			"a short command finishes at the shell prompt",
			[]string{"running", "idle", "running", "just_finished", "idle", "running", "just_finished", "exited"},
			corpusStep{wait: time.Second, input: "echo hello"},
			corpusStep{wait: 300 * time.Millisecond, input: "\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}),
		bash("long-build", "recorded: bash with the NexTerm integration",
			"a build loop prints for twelve seconds, then finishes notable",
			nil,
			corpusStep{wait: time.Second, input: "for i in $(seq 1 24); do echo compiling unit $i; sleep 0.5; done\r"},
			corpusStep{wait: 14 * time.Second, input: "exit\r"}),
		bash("program-bell", "recorded: bash with the NexTerm integration",
			"a program rings the bell after working and waits for a line",
			nil,
			corpusStep{wait: time.Second, input: "sleep 2; printf 'review needed\\a\\n'; read reply\r"},
			corpusStep{wait: 4 * time.Second, input: "ok\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}),
		bash("osc9-notify", "recorded: bash with the NexTerm integration",
			"a program sends an OSC 9 notification and waits for a line",
			nil,
			corpusStep{wait: time.Second, input: "sleep 1.5; printf '\\033]9;Build finished\\007'; read reply\r"},
			corpusStep{wait: 3500 * time.Millisecond, input: "\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}),
		job(bash("yesno-prompt", "recorded: bash running a python stand-in",
			"a line-mode [y/N] confirmation is answered",
			nil,
			corpusStep{wait: time.Second, input: "python3 ask.py\r"},
			corpusStep{wait: 2 * time.Second, input: "y\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}), true),
		job(bash("numbered-menu", "recorded: bash running a python stand-in",
			"a numbered list without a pointer asks for a line",
			nil,
			corpusStep{wait: time.Second, input: "python3 menu.py\r"},
			corpusStep{wait: 2 * time.Second, input: "1\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}), true),
		job(bash("password-question", "recorded: bash running a python stand-in",
			"a password question is attention-worthy but never tappable",
			nil,
			corpusStep{wait: time.Second, input: "python3 password.py\r"},
			corpusStep{wait: 2 * time.Second, input: "not-a-secret\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}), true),
		job(bash("spinner-thinking", "recorded: bash running a python stand-in",
			"a spinner redraws for four seconds without any prompt",
			nil,
			corpusStep{wait: time.Second, input: "python3 spinner.py\r"},
			corpusStep{wait: 6 * time.Second, input: "exit\r"}), true),
		job(bash("tui-marked-menu", "recorded: bash running a python stand-in",
			"a raw-mode program works, then shows a framed menu with a pointer",
			nil,
			corpusStep{wait: time.Second, input: "python3 tui.py\r"},
			corpusStep{wait: 4 * time.Second, input: "\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}), false),
		job(bash("tui-menu-notify", "recorded: bash running a python stand-in",
			"the framed menu program also sends an OSC 9 notification",
			nil,
			corpusStep{wait: time.Second, input: "python3 tui.py --notify\r"},
			corpusStep{wait: 4 * time.Second, input: "\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}), false),
		job(bash("vim-edit", "recorded: bash running vim -u NONE",
			"vim opens, text is typed, vim quits",
			nil,
			corpusStep{wait: time.Second, input: "vim -u NONE -N notes.txt\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "ihello"},
			corpusStep{wait: 1000 * time.Millisecond, input: "\x1b"},
			corpusStep{wait: 1000 * time.Millisecond, input: ":q!\r"},
			corpusStep{wait: 1500 * time.Millisecond, input: "exit\r"}), false),
	}
	for i := range scenarios {
		scenarios[i].scripts = map[string]string{
			"ask.py": scriptAsk, "menu.py": scriptMenu, "password.py": scriptPassword,
			"spinner.py": scriptSpinner, "tui.py": scriptTUI,
		}
	}
	return scenarios
}

// TestRecordStateCorpus regenerates the recorded corpus. The labels in the
// written headers are kept from the existing files when present, so a
// re-recording is checked against the reviewed expectations.
func TestRecordStateCorpus(t *testing.T) {
	if os.Getenv(recordCorpusEnv) != "1" {
		t.Skip("set " + recordCorpusEnv + "=1 to re-record testdata/states")
	}
	supervisor := testSupervisor(t)
	for _, scenario := range corpusScenarios() {
		t.Run(scenario.header.Name, func(t *testing.T) {
			recordScenario(t, supervisor, scenario)
		})
	}
}

func recordScenario(t *testing.T, supervisor *Supervisor, scenario corpusScenario) {
	work := t.TempDir()
	home := t.TempDir()
	for name, body := range scenario.scripts {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := scenario.command
	if command == nil {
		command = []string{"/bin/bash", "-i"}
	}
	session, err := supervisor.Create(context.Background(), CreateOptions{
		ID:      ids.New(),
		Command: command,
		Dir:     work,
		Env: []string{
			"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=" + home,
			"PS1=\\$ ", "HISTFILE=/dev/null", "USER=user", "LOGNAME=user",
		},
		Cols: uint32(scenario.header.Cols),
		Rows: uint32(scenario.header.Rows),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	attachment, err := session.attach(AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines [][]any
	stamp := func() float64 { return float64(time.Since(start).Milliseconds()) / 1000 }
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32*1024)
		for {
			count, err := attachment.Read(buffer)
			if count > 0 {
				mu.Lock()
				lines = append(lines, []any{stamp(), "o", string(buffer[:count])})
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	for _, step := range scenario.steps {
		time.Sleep(step.wait)
		mu.Lock()
		lines = append(lines, []any{stamp(), "i", step.input})
		mu.Unlock()
		if _, err := session.Write([]byte(step.input)); err != nil {
			t.Fatal(err)
		}
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), scenario.tail+5*time.Second)
	defer cancel()
	waitErr := session.Wait(waitCtx)
	<-done
	code := 0
	if info := session.Info(); info.ExitCode != nil {
		code = *info.ExitCode
	} else if waitErr != nil {
		t.Fatalf("session did not exit: %v", waitErr)
	}
	mu.Lock()
	lines = append(lines, []any{stamp(), "x", code})
	mu.Unlock()

	path := filepath.Join("testdata", "states", scenario.header.Name+".jsonl")
	header := scenario.header
	if existing, err := os.ReadFile(path); err == nil {
		var previous corpusHeader
		first, _, _ := strings.Cut(string(existing), "\n")
		if json.Unmarshal([]byte(first), &previous) == nil && len(previous.Expect) > 0 {
			header.Expect, header.ExpectPrompt, header.KnownMiss = previous.Expect, previous.ExpectPrompt, previous.KnownMiss
		}
	}
	if len(header.Expect) == 0 {
		header.Expect = []string{"unlabelled"}
	}
	sanitize := corpusSanitizer(work, home)
	var out strings.Builder
	encoded, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	out.Write(encoded)
	out.WriteByte('\n')
	for _, line := range lines {
		if text, ok := line[2].(string); ok {
			line[2] = sanitize(text)
		}
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func corpusSanitizer(work, home string) func(string) string {
	var pairs []string
	for _, dir := range []string{work, home} {
		pairs = append(pairs, dir, "/home/user/project")
		if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
			pairs = append(pairs, resolved, "/home/user/project")
		}
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		pairs = append(pairs, host, "host")
	}
	// A user name is replaced only where it names the user, so common
	// words such as "dev" stay intact elsewhere.
	for _, key := range []string{"USER", "LOGNAME"} {
		if name := os.Getenv(key); name != "" {
			pairs = append(pairs, name+"@", "user@", "/home/"+name, "/home/user", "/Users/"+name, "/Users/user")
		}
	}
	replacer := strings.NewReplacer(pairs...)
	return replacer.Replace
}
