package winrm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// interactivePowerShell reads one statement per stdin line and keeps its
// session state (variables, functions, modules, location) between them.
const interactivePowerShell = "powershell.exe -NoLogo -NoProfile -NonInteractive -InputFormat Text -OutputFormat Text -Command -"

var (
	ErrShellClosed      = errors.New("WinRM PowerShell session closed")
	ErrShellLimit       = errors.New("WinRM shell quota exceeded (MaxShellsPerUser)")
	ErrInterruptTimeout = errors.New("PowerShell did not stop after Ctrl+C")
	ErrShellUnsupported = errors.New("WinRM interactive PowerShell is unavailable")
)

// ShellExitError reports that the PowerShell process ended, for example after
// the user ran exit.
type ShellExitError struct {
	Code int
}

func (e *ShellExitError) Error() string {
	return fmt.Sprintf("PowerShell exited with code %d", e.Code)
}

// ShellOptions configures an interactive shell. Output receives decoded
// stdout and stderr text in arrival order, without completion markers.
type ShellOptions struct {
	Output func(stderr bool, text string)
}

// CommandResult describes a finished interactive command: Success is $?,
// ExitCode is 0 on success and otherwise $LASTEXITCODE or 1, and CWD is the
// PowerShell location after the command.
type CommandResult struct {
	ExitCode int
	Success  bool
	CWD      string
}

// InteractiveShell is one long-lived PowerShell process in a WinRM shell.
// Run executes one command at a time; Interrupt stops the running one.
type InteractiveShell interface {
	Run(ctx context.Context, command string) (CommandResult, error)
	Interrupt(ctx context.Context) error
	Done() <-chan struct{}
	Err() error
	Close() error
}

type shellRunner interface {
	createShell(context.Context) (string, error)
	executeCommand(context.Context, string, string) (string, error)
	sendInput(context.Context, string, string, []byte) error
	receiveStreams(context.Context, string, string, time.Duration) (receiveResult, error)
	signal(context.Context, string, string, string) error
	deleteShell(context.Context, string) error
	Close() error
}

type shellTimings struct {
	receiveWait    time.Duration
	interruptGrace time.Duration
	stderrGrace    time.Duration
	closeTimeout   time.Duration
}

func defaultShellTimings(requestTimeout time.Duration) shellTimings {
	wait := min(20*time.Second, requestTimeout/2)
	if wait < time.Second {
		wait = time.Second
	}
	return shellTimings{
		receiveWait:    wait,
		interruptGrace: 5 * time.Second,
		stderrGrace:    2 * time.Second,
		closeTimeout:   min(10*time.Second, max(requestTimeout, time.Second)),
	}
}

// psShell drives the WS-Management shell lifecycle for one PowerShell
// process. The control runner carries Create, Command, Send, Signal and
// Delete; the poll runner carries only the long-polling Receive, so input and
// Ctrl+C are never queued behind it on a single NTLM connection.
type psShell struct {
	control  shellRunner
	poll     shellRunner
	output   func(bool, string)
	onCWD    func(string)
	timings  shellTimings
	release  func()
	shellID  string
	command  string
	life     context.Context
	stopLife context.CancelFunc
	dead     chan struct{}
	pollDone chan struct{}

	runMu sync.Mutex

	mu       sync.Mutex
	pending  *shellCommand
	stdout   markerScanner
	stderr   markerScanner
	outText  textDecoder
	errText  textDecoder
	err      error
	exited   bool
	deadOnce sync.Once

	closeOnce sync.Once
	closeErr  error
}

type shellCommand struct {
	token       string
	quiet       bool
	outSeen     bool
	errSeen     bool
	result      CommandResult
	resultErr   error
	interrupted bool
	done        chan struct{}
	timers      []*time.Timer
}

func newPSShell(control, poll shellRunner, options ShellOptions, onCWD func(string), timings shellTimings) *psShell {
	life, stop := context.WithCancel(context.Background())
	output := options.Output
	if output == nil {
		output = func(bool, string) {}
	}
	return &psShell{
		control: control, poll: poll, output: output, onCWD: onCWD, timings: timings,
		life: life, stopLife: stop, dead: make(chan struct{}), pollDone: make(chan struct{}),
	}
}

func (s *psShell) start(ctx context.Context, cwd string) error {
	// Until it is ready, the caller's context bounds the whole shell.
	stop := context.AfterFunc(ctx, func() { s.fail(context.Cause(ctx)) })
	defer stop()
	shellID, err := s.control.createShell(s.life)
	if err != nil {
		close(s.pollDone)
		if isShellQuota(err) {
			return fmt.Errorf("%w: %v", ErrShellLimit, err)
		}
		return fmt.Errorf("create WinRM shell: %w", err)
	}
	s.shellID = shellID
	commandID, err := s.control.executeCommand(s.life, shellID, interactivePowerShell)
	if err != nil {
		close(s.pollDone)
		if isShellQuota(err) {
			return fmt.Errorf("%w: %v", ErrShellLimit, err)
		}
		return fmt.Errorf("start PowerShell: %w", err)
	}
	s.command = commandID
	go s.pollLoop()
	script := encodingPrefix + "$ProgressPreference = 'SilentlyContinue'\n"
	if cwd != "" {
		script += "Set-Location -LiteralPath " + quoteLiteral(cwd) + " -ErrorAction SilentlyContinue\n"
	}
	if _, err := s.run(ctx, script, true); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("initialize PowerShell: %w", err)
	}
	return nil
}

func (s *psShell) Done() <-chan struct{} {
	return s.dead
}

func (s *psShell) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *psShell) fail(err error) {
	s.deadOnce.Do(func() {
		if err == nil {
			err = ErrShellClosed
		}
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.dead)
		s.stopLife()
	})
}

func (s *psShell) Run(ctx context.Context, command string) (CommandResult, error) {
	return s.run(ctx, command, false)
}

func (s *psShell) run(ctx context.Context, command string, quiet bool) (CommandResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	select {
	case <-s.dead:
		return CommandResult{}, s.Err()
	default:
	}
	command = strings.TrimRight(command, "\r\n")
	pending := &shellCommand{token: newShellToken(), quiet: quiet, done: make(chan struct{})}
	s.mu.Lock()
	s.pending = pending
	s.stdout.arm(outputStartMarker(pending.token), outputEndMarker(pending.token))
	s.stderr.arm(errorMarker(pending.token), nil)
	s.mu.Unlock()
	// The send is bound to the shell, not the caller: a half-sent line would
	// leave the process in an unknown state.
	if err := s.control.sendInput(s.life, s.shellID, s.command, []byte(interactiveCommandLine(pending.token, command))); err != nil {
		s.fail(fmt.Errorf("send PowerShell input: %w", err))
		s.abandon(pending)
		return CommandResult{}, s.Err()
	}
	interrupted := false
	ctxDone := ctx.Done()
	for {
		select {
		case <-pending.done:
			if pending.resultErr != nil {
				s.fail(pending.resultErr)
				return CommandResult{}, pending.resultErr
			}
			if s.onCWD != nil && pending.result.CWD != "" {
				s.onCWD(pending.result.CWD)
			}
			if interrupted {
				return pending.result, ctx.Err()
			}
			return pending.result, nil
		case <-s.dead:
			select {
			case <-pending.done:
				continue
			default:
			}
			s.abandon(pending)
			return CommandResult{}, s.Err()
		case <-ctxDone:
			ctxDone = nil
			interrupted = true
			_ = s.Interrupt(context.WithoutCancel(ctx))
		}
	}
}

func (s *psShell) abandon(pending *shellCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == pending {
		s.pending = nil
		pending.stopTimersLocked()
	}
}

// Interrupt sends Ctrl+C to the running command. A command still running
// after the grace period is ended by terminating the PowerShell process, which
// ends this shell with ErrInterruptTimeout.
func (s *psShell) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	pending := s.pending
	if pending == nil || pending.interrupted {
		s.mu.Unlock()
		return nil
	}
	pending.interrupted = true
	s.mu.Unlock()
	signalCtx, cancel := context.WithTimeout(s.life, s.timings.closeTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	err := s.control.signal(signalCtx, s.shellID, s.command, signalCtrlC)
	if err != nil {
		s.killIfPending(pending)
		return fmt.Errorf("send Ctrl+C: %w", err)
	}
	s.mu.Lock()
	if s.pending == pending {
		pending.timers = append(pending.timers, time.AfterFunc(s.timings.interruptGrace, func() { s.killIfPending(pending) }))
	}
	s.mu.Unlock()
	return nil
}

func (s *psShell) killIfPending(pending *shellCommand) {
	s.mu.Lock()
	current := s.pending == pending
	s.mu.Unlock()
	if current {
		s.fail(ErrInterruptTimeout)
	}
}

// Close ends the PowerShell process and deletes the remote shell, bounded by
// the close timeout.
func (s *psShell) Close() error {
	s.closeOnce.Do(func() {
		s.fail(ErrShellClosed)
		<-s.pollDone
		s.mu.Lock()
		exited := s.exited
		if s.pending != nil {
			s.pending.stopTimersLocked()
		}
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), s.timings.closeTimeout)
		defer cancel()
		var errs []error
		if s.command != "" && !exited {
			// Deleting the shell ends its processes too; the signal only
			// shortens that, so its failure is not reported.
			_ = s.control.signal(ctx, s.shellID, s.command, signalTerminate)
		}
		if s.shellID != "" {
			if err := s.control.deleteShell(ctx, s.shellID); err != nil {
				errs = append(errs, fmt.Errorf("delete WinRM shell: %w", err))
			}
		}
		errs = append(errs, s.control.Close(), s.poll.Close())
		if s.release != nil {
			s.release()
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

func (s *psShell) pollLoop() {
	defer close(s.pollDone)
	for {
		result, err := s.poll.receiveStreams(s.life, s.shellID, s.command, s.timings.receiveWait)
		if err != nil {
			if s.life.Err() != nil {
				return
			}
			if isOperationTimeout(err) {
				continue
			}
			s.fail(fmt.Errorf("receive PowerShell output: %w", err))
			return
		}
		for _, chunk := range result.chunks {
			s.handleChunk(chunk)
		}
		if result.done {
			s.mu.Lock()
			s.exited = true
			s.mu.Unlock()
			s.flushText()
			s.fail(&ShellExitError{Code: result.exitCode})
			return
		}
	}
}

func (s *psShell) handleChunk(chunk streamChunk) {
	data := chunk.data
	for len(data) > 0 {
		s.mu.Lock()
		scanner, decoder := &s.stdout, &s.outText
		if chunk.stderr {
			scanner, decoder = &s.stderr, &s.errText
		}
		pending := s.pending
		quiet := pending != nil && pending.quiet
		before, found, payload, after := scanner.scan(data)
		text := decoder.decode(before, found)
		s.mu.Unlock()
		if text != "" && !quiet {
			s.output(chunk.stderr, text)
		}
		if !found {
			return
		}
		s.complete(pending, chunk.stderr, payload)
		data = after
	}
}

func (s *psShell) flushText() {
	s.mu.Lock()
	outText := s.outText.decode(s.stdout.drain(), true)
	errText := s.errText.decode(s.stderr.drain(), true)
	s.mu.Unlock()
	if outText != "" {
		s.output(false, outText)
	}
	if errText != "" {
		s.output(true, errText)
	}
}

func (s *psShell) complete(pending *shellCommand, stderr bool, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending == nil || s.pending != pending {
		return
	}
	if stderr {
		pending.errSeen = true
	} else {
		pending.outSeen = true
		pending.result, pending.resultErr = parseCompletion(payload)
	}
	if pending.outSeen && pending.errSeen {
		s.finishLocked(pending)
		return
	}
	if pending.outSeen {
		// stdout and stderr are separate pipes: give the stderr marker a
		// moment so error text written before it is still shown before the
		// prompt, without waiting forever if it never arrives.
		pending.timers = append(pending.timers, time.AfterFunc(s.timings.stderrGrace, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.pending == pending {
				s.finishLocked(pending)
			}
		}))
	}
}

func (s *psShell) finishLocked(pending *shellCommand) {
	s.pending = nil
	pending.stopTimersLocked()
	close(pending.done)
}

func (c *shellCommand) stopTimersLocked() {
	for _, timer := range c.timers {
		timer.Stop()
	}
	c.timers = nil
}

func newShellToken() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}

func outputStartMarker(token string) []byte { return []byte("<<NXT:" + token + ">>") }
func outputEndMarker(token string) []byte   { return []byte("<</NXT:" + token + ">>") }
func errorMarker(token string) []byte       { return []byte("<<NXE:" + token + ">>") }

// interactiveCommandLine wraps one command as a single ASCII stdin line. The
// command travels base64-encoded, so its quoting and input encoding cannot
// break the line, and an incomplete statement fails on its own instead of
// swallowing the next line. It runs dot-sourced at top level, so its state
// persists. Out-Default renders its output before the markers; the finally
// block writes the markers even when Ctrl+C stops the pipeline. The stdout
// marker carries $?, the exit code and the base64 location.
func interactiveCommandLine(token, command string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	return ". { $__nextermOK = $false; try { Invoke-Expression ([System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('" + encoded + "'))) | Out-Default; $__nextermOK = $? } " +
		"catch { Write-Error -ErrorRecord $_ -ErrorAction Continue } " +
		"finally { $__nextermCode = if ($__nextermOK) { 0 } elseif ($LASTEXITCODE -is [int] -and $LASTEXITCODE -ne 0) { $LASTEXITCODE } else { 1 }; " +
		"[Console]::Error.Write('" + string(errorMarker(token)) + "'); [Console]::Error.Flush(); " +
		"[Console]::Out.Write('" + string(outputStartMarker(token)) + "' + [int]$__nextermOK + ':' + $__nextermCode + ':' + " +
		"[System.Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($ExecutionContext.SessionState.Path.CurrentLocation.Path)) + '" +
		string(outputEndMarker(token)) + "'); [Console]::Out.Flush() } }\r\n"
}

func parseCompletion(payload []byte) (CommandResult, error) {
	parts := strings.SplitN(strings.TrimSpace(string(payload)), ":", 3)
	if len(parts) != 3 {
		return CommandResult{}, fmt.Errorf("malformed PowerShell completion %q", payload)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return CommandResult{}, fmt.Errorf("malformed PowerShell exit code %q", parts[1])
	}
	location, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return CommandResult{}, fmt.Errorf("malformed PowerShell location: %w", err)
	}
	return CommandResult{ExitCode: code, Success: parts[0] == "1", CWD: string(location)}, nil
}

// maxMarkerPayload bounds a marker whose end never arrives.
const maxMarkerPayload = 64 << 10

// markerScanner finds one marker (start, and end when the marker carries a
// payload) in a byte stream split at arbitrary points. A suffix that could
// begin the marker is held back until the next chunk decides it.
type markerScanner struct {
	start   []byte
	end     []byte
	held    []byte
	inside  bool
	payload []byte
}

func (m *markerScanner) arm(start, end []byte) {
	if m.inside {
		m.held = append(m.held, m.payload...)
	}
	m.start, m.end, m.inside, m.payload = start, end, false, nil
}

func (m *markerScanner) drain() []byte {
	held := append(m.held, m.payload...)
	m.held, m.payload, m.inside = nil, nil, false
	return held
}

// scan returns the output before the marker, whether the marker completed,
// its payload and the bytes after it. A completed marker disarms the scanner.
func (m *markerScanner) scan(data []byte) (before []byte, found bool, payload []byte, after []byte) {
	buf := make([]byte, 0, len(m.held)+len(data))
	buf = append(append(buf, m.held...), data...)
	m.held = nil
	if len(m.start) == 0 {
		return buf, false, nil, nil
	}
	if !m.inside {
		index := bytes.Index(buf, m.start)
		if index < 0 {
			keep := markerPrefixSuffix(buf, m.start)
			m.held = append(m.held, buf[len(buf)-keep:]...)
			return buf[:len(buf)-keep], false, nil, nil
		}
		before = buf[:index]
		buf = buf[index+len(m.start):]
		if len(m.end) == 0 {
			m.start = nil
			return before, true, nil, buf
		}
		m.inside = true
	}
	m.payload = append(m.payload, buf...)
	index := bytes.Index(m.payload, m.end)
	if index < 0 {
		if len(m.payload) > maxMarkerPayload {
			// Not a marker after all: release it as output.
			released := append(before, m.drain()...)
			m.start, m.end = nil, nil
			return released, false, nil, nil
		}
		return before, false, nil, nil
	}
	payload = m.payload[:index]
	after = m.payload[index+len(m.end):]
	m.start, m.end, m.inside, m.payload = nil, nil, false, nil
	return before, true, payload, after
}

func markerPrefixSuffix(buf, marker []byte) int {
	for keep := min(len(buf), len(marker)-1); keep > 0; keep-- {
		if bytes.HasPrefix(marker, buf[len(buf)-keep:]) {
			return keep
		}
	}
	return 0
}

// textDecoder decodes a byte stream chunk by chunk, holding back an
// incomplete trailing UTF-8 sequence so a character split across Receive
// responses is not garbled.
type textDecoder struct {
	carry []byte
}

func (d *textDecoder) decode(data []byte, flush bool) string {
	buf := make([]byte, 0, len(d.carry)+len(data))
	buf = append(append(buf, d.carry...), data...)
	d.carry = nil
	if !flush {
		cut := incompleteUTF8Suffix(buf)
		d.carry = append(d.carry, buf[len(buf)-cut:]...)
		buf = buf[:len(buf)-cut]
	}
	if len(buf) == 0 {
		return ""
	}
	return decodeBytes(buf)
}

func incompleteUTF8Suffix(buf []byte) int {
	for back := 1; back <= min(3, len(buf)); back++ {
		b := buf[len(buf)-back]
		if b < 0x80 {
			return 0
		}
		if utf8.RuneStart(b) {
			if utf8.FullRune(buf[len(buf)-back:]) {
				return 0
			}
			return back
		}
	}
	return 0
}
