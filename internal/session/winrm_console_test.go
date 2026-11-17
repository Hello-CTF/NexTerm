package session

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/winrm"
)

// fakeWinRMTransport is a WinRM session transport whose OpenShell starts
// in-memory PowerShell stand-ins.
type fakeWinRMTransport struct {
	*fakeTransport
	openErr error
	mu      sync.Mutex
	shells  []*fakeInteractiveShell
	cwd     string
}

func (t *fakeWinRMTransport) CWD() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

func (t *fakeWinRMTransport) OpenShell(ctx context.Context, options winrm.ShellOptions) (winrm.InteractiveShell, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openErr != nil {
		return nil, t.openErr
	}
	shell := &fakeInteractiveShell{
		transport: t, output: options.Output, vars: make(map[string]string),
		dead: make(chan struct{}), release: make(chan struct{}, 8), interrupted: make(chan struct{}, 8), running: make(chan string, 8),
	}
	t.shells = append(t.shells, shell)
	return shell, nil
}

func (t *fakeWinRMTransport) shell(index int) *fakeInteractiveShell {
	t.mu.Lock()
	defer t.mu.Unlock()
	if index >= len(t.shells) {
		return nil
	}
	return t.shells[index]
}

func (t *fakeWinRMTransport) shellCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.shells)
}

type fakeInteractiveShell struct {
	transport   *fakeWinRMTransport
	output      func(bool, string)
	release     chan struct{}
	interrupted chan struct{}
	running     chan string
	closes      atomic.Int32
	interrupts  atomic.Int32

	mu       sync.Mutex
	vars     map[string]string
	commands []string
	dead     chan struct{}
	deadOnce sync.Once
	err      error
}

func (s *fakeInteractiveShell) die(err error) {
	s.deadOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.dead)
	})
}

func (s *fakeInteractiveShell) Run(ctx context.Context, command string) (winrm.CommandResult, error) {
	select {
	case <-s.dead:
		return winrm.CommandResult{}, s.Err()
	default:
	}
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	cwd := func() string { return s.transport.CWD() }
	ok := winrm.CommandResult{Success: true}
	fields := strings.Fields(command)
	switch {
	case len(fields) == 3 && fields[1] == "=":
		s.mu.Lock()
		s.vars[fields[0]] = fields[2]
		s.mu.Unlock()
	case len(fields) == 2 && fields[0] == "echo":
		value := fields[1]
		if strings.HasPrefix(value, "$") {
			s.mu.Lock()
			value = s.vars[value]
			s.mu.Unlock()
		}
		s.output(false, value+"\n")
	case len(fields) == 2 && fields[0] == "cd":
		s.transport.mu.Lock()
		s.transport.cwd = fields[1]
		s.transport.mu.Unlock()
	case command == "err":
		s.output(true, "bad\n")
		return winrm.CommandResult{ExitCode: 1, CWD: cwd()}, nil
	case command == "block":
		s.running <- command
		select {
		case <-s.release:
			s.output(false, "released\n")
		case <-s.interrupted:
			return winrm.CommandResult{ExitCode: 1, CWD: cwd()}, nil
		case <-s.dead:
			return winrm.CommandResult{}, s.Err()
		case <-ctx.Done():
			return winrm.CommandResult{}, ctx.Err()
		}
	case len(fields) == 2 && fields[0] == "exit":
		code, _ := strconv.Atoi(fields[1])
		s.die(&winrm.ShellExitError{Code: code})
		return winrm.CommandResult{}, s.Err()
	default:
		s.output(false, "ran "+strings.ReplaceAll(command, "\n", "|")+"\n")
	}
	ok.CWD = cwd()
	return ok, nil
}

func (s *fakeInteractiveShell) Interrupt(context.Context) error {
	s.interrupts.Add(1)
	s.interrupted <- struct{}{}
	return nil
}

func (s *fakeInteractiveShell) Done() <-chan struct{} { return s.dead }

func (s *fakeInteractiveShell) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *fakeInteractiveShell) Close() error {
	s.closes.Add(1)
	s.die(winrm.ErrShellClosed)
	return nil
}

func (s *fakeInteractiveShell) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

type winRMConsoleFixture struct {
	manager    *Manager
	terminals  *fakeTerminalFactory
	session    *Session
	tab        *Tab
	transports []*fakeWinRMTransport
	mu         sync.Mutex
}

func newWinRMConsoleFixture(t *testing.T, openErr error) *winRMConsoleFixture {
	t.Helper()
	fixture := &winRMConsoleFixture{terminals: newFakeTerminalFactory()}
	connector := newFakeConnector()
	connector.connect = func(_ context.Context, asset Asset, _ uint64, call int) (base.Transport, error) {
		transport := &fakeWinRMTransport{fakeTransport: newFakeTransport(asset.Kind, uint64(call)+1000), openErr: openErr, cwd: `C:\`}
		fixture.mu.Lock()
		fixture.transports = append(fixture.transports, transport)
		fixture.mu.Unlock()
		return transport, nil
	}
	fixture.manager = NewManager(Config{Connector: connector, Terminals: fixture.terminals, ReconnectMax: 2, ReconnectBackoff: []time.Duration{0}})
	t.Cleanup(func() { _ = fixture.manager.Close() })
	session, err := fixture.manager.Connect(context.Background(), Asset{ID: "asset-winrm", Name: "win", Kind: KindWinRM})
	if err != nil {
		t.Fatal(err)
	}
	fixture.session = session
	fixture.tab = openTestTab(t, fixture.manager, session, "client-a", "a-1")
	return fixture
}

func (f *winRMConsoleFixture) transport(index int) *fakeWinRMTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[index]
}

func (f *winRMConsoleFixture) screen() string {
	return string(f.terminals.terminal(f.tab.ID).bytes())
}

func (f *winRMConsoleFixture) waitScreen(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(f.screen(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("screen never showed %q:\n%q", want, f.screen())
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *winRMConsoleFixture) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.tab.winrm.mu.Lock()
		busy := f.tab.winrm.busy
		f.tab.winrm.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("WinRM console stayed busy")
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *winRMConsoleFixture) typeText(t *testing.T, text string) {
	t.Helper()
	if err := f.manager.Write(context.Background(), f.tab.ID, "client-a", []byte(text)); err != nil {
		t.Fatalf("Write(%q): %v", text, err)
	}
}

func TestWinRMConsoleKeepsStateInOnePowerShell(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, string(winRMBanner)+`PS C:\> `)
	fixture.typeText(t, "$x = 42\r")
	fixture.waitIdle(t)
	fixture.typeText(t, "echo $x\r")
	fixture.waitScreen(t, "PS C:\\> echo $x\r\n42\r\nPS C:\\> ")
	transport := fixture.transport(0)
	if count := transport.shellCount(); count != 1 {
		t.Fatalf("opened %d PowerShells, want 1", count)
	}
	if got := transport.shell(0).ran(); strings.Join(got, ";") != "$x = 42;echo $x" {
		t.Fatalf("commands = %q", got)
	}
	transport.fakeTransport.mu.Lock()
	execs := len(transport.execs)
	transport.fakeTransport.mu.Unlock()
	if execs != 0 {
		t.Fatalf("typed lines went through Exec %d times", execs)
	}
}

func TestWinRMConsoleEditsLineAndShowsStderr(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	fixture.typeText(t, "erx\x7fr")
	fixture.typeText(t, "\r")
	fixture.waitScreen(t, "\x1b[31mbad\r\n\x1b[0mPS C:\\> ")
	if got := fixture.transport(0).shell(0).ran(); len(got) != 1 || got[0] != "err" {
		t.Fatalf("commands = %q", got)
	}
	if !strings.Contains(fixture.screen(), "erx\b \br\r\n") {
		t.Fatalf("backspace echo missing: %q", fixture.screen())
	}
}

func TestWinRMConsoleTracksLocationInPrompt(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	result, err := fixture.manager.ExecLine(context.Background(), fixture.tab.ID, "client-a", `cd D:\work`)
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("ExecLine = %+v, %v", result, err)
	}
	fixture.waitScreen(t, "PS C:\\> cd D:\\work\r\nPS D:\\work> ")
}

func TestWinRMConsoleCtrlCInterruptsRunningCommand(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	fixture.typeText(t, "block\r")
	shell := fixture.transport(0).shell(0)
	<-shell.running
	fixture.typeText(t, "echo dropped\r\x03")
	fixture.waitIdle(t)
	fixture.waitScreen(t, "block\r\n^C\r\nPS C:\\> ")
	if shell.interrupts.Load() != 1 {
		t.Fatalf("interrupts = %d", shell.interrupts.Load())
	}
	if got := shell.ran(); len(got) != 1 {
		t.Fatalf("Ctrl+C must drop lines typed ahead: %q", got)
	}
	fixture.typeText(t, "\x03")
	fixture.waitScreen(t, "PS C:\\> ^C\r\nPS C:\\> ")
	if shell.interrupts.Load() != 1 {
		t.Fatal("Ctrl+C at the prompt interrupted the shell")
	}
}

func TestWinRMConsoleRunsTypeAheadAfterCommand(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	fixture.typeText(t, "block\r")
	shell := fixture.transport(0).shell(0)
	<-shell.running
	fixture.typeText(t, "echo later\recho pa")
	if strings.Contains(fixture.screen(), "later") {
		t.Fatalf("type-ahead echoed while the command runs: %q", fixture.screen())
	}
	shell.release <- struct{}{}
	fixture.waitIdle(t)
	fixture.waitScreen(t, "block\r\nreleased\r\nPS C:\\> echo later\r\nlater\r\nPS C:\\> echo pa")
}

func TestWinRMConsoleJoinsContinuationLines(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	fixture.typeText(t, "if ($a) {\r")
	fixture.typeText(t, "  go\r")
	fixture.typeText(t, "}\r")
	fixture.waitScreen(t, "PS C:\\> if ($a) {\r\n>>   go\r\n>> }\r\nran if ($a) {|  go|}\r\nPS C:\\> ")
}

func TestWinRMConsoleFallsBackToLineMode(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		notice string
	}{
		{name: "shell quota", err: winrm.ErrShellLimit, notice: winRMShellLimitNotice},
		{name: "other failure", err: errors.New("access denied"), notice: "已切换为行模式（每条命令在新的 shell 中运行）：access denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWinRMConsoleFixture(t, test.err)
			fixture.waitScreen(t, test.notice+"\x1b[0m\r\nPS C:\\> ")
			fixture.typeText(t, "whoami\r")
			fixture.waitScreen(t, "whoami\r\nran:whoami\r\nPS C:\\> ")
			transport := fixture.transport(0)
			transport.fakeTransport.mu.Lock()
			execs := append([]string(nil), transport.execs...)
			transport.fakeTransport.mu.Unlock()
			if len(execs) != 1 || execs[0] != "whoami" {
				t.Fatalf("line mode execs = %q", execs)
			}
			if strings.Count(fixture.screen(), test.notice) != 1 {
				t.Fatalf("fallback notice not shown exactly once: %q", fixture.screen())
			}
		})
	}
}

func TestWinRMConsoleRestartsExitedPowerShell(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	fixture.typeText(t, "exit 3\r")
	fixture.waitScreen(t, "（退出码 3）")
	fixture.waitIdle(t)
	transport := fixture.transport(0)
	if transport.shellCount() != 2 || transport.shell(0).closes.Load() != 1 {
		t.Fatalf("shells = %d, first closed %d times", transport.shellCount(), transport.shell(0).closes.Load())
	}
	fixture.typeText(t, "echo again\r")
	fixture.waitScreen(t, "again\r\nPS C:\\> ")
	if got := transport.shell(1).ran(); len(got) != 1 || got[0] != "echo again" {
		t.Fatalf("new shell commands = %q", got)
	}
}

func TestWinRMConsoleClosesShellWithTab(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	shell := fixture.transport(0).shell(0)
	if err := fixture.manager.CloseTab(fixture.tab.ID); err != nil {
		t.Fatal(err)
	}
	if shell.closes.Load() != 1 {
		t.Fatalf("shell closes = %d", shell.closes.Load())
	}
}

func TestWinRMConsoleCloseInterruptsRunningCommand(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	fixture.typeText(t, "block\r")
	shell := fixture.transport(0).shell(0)
	<-shell.running
	pending := make(chan error, 1)
	go func() {
		_, err := fixture.manager.ExecLine(context.Background(), fixture.tab.ID, "client-a", "echo queued")
		pending <- err
	}()
	waitFor(t, func() bool {
		fixture.tab.winrm.mu.Lock()
		defer fixture.tab.winrm.mu.Unlock()
		return len(fixture.tab.winrm.queue) == 1
	})
	if err := fixture.manager.CloseTab(fixture.tab.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-pending; !errors.Is(err, ErrTabClosed) {
		t.Fatalf("queued ExecLine error = %v", err)
	}
	if shell.closes.Load() != 1 {
		t.Fatalf("shell closes = %d", shell.closes.Load())
	}
}

func TestWinRMConsoleOpensNewShellAfterReconnect(t *testing.T) {
	fixture := newWinRMConsoleFixture(t, nil)
	fixture.waitScreen(t, `PS C:\> `)
	first := fixture.transport(0).shell(0)
	if err := fixture.manager.Reconnect(context.Background(), fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	fixture.typeText(t, "echo after\r")
	fixture.waitScreen(t, "after\r\nPS C:\\> ")
	if first.closes.Load() != 1 {
		t.Fatalf("old shell closes = %d", first.closes.Load())
	}
	if got := fixture.transport(1).shell(0).ran(); len(got) != 1 || got[0] != "echo after" {
		t.Fatalf("new transport shell commands = %q", got)
	}
	if bytes.Contains([]byte(fixture.screen()), []byte("清空")) {
		t.Fatalf("a reconnect is not a lost session: %q", fixture.screen())
	}
}

func TestWinRMIncomplete(t *testing.T) {
	for text, want := range map[string]bool{
		"Get-Process":                 false,
		"if ($a) {":                   true,
		"if ($a) {\n  go\n}":          false,
		"Get-Process |":               true,
		"Write-Output a `":            true,
		"'it''s {'":                   false,
		"\"open":                      true,
		"$s = @'":                     true,
		"$s = @'\n{\n'@":              false,
		"Get-Item x # {":              false,
		"\"escaped `\" {\"":           false,
		"foreach ($i in 1..3) { $i }": false,
	} {
		if got := winRMIncomplete(text); got != want {
			t.Errorf("winRMIncomplete(%q) = %v, want %v", text, got, want)
		}
	}
}
