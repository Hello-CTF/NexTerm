package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/winrm"
	"golang.org/x/text/width"
)

var winRMBanner = []byte("[NexTerm] WinRM PowerShell 会话：命令在同一个 PowerShell 中运行，\r\n变量、函数、模块和当前目录都会保留；Ctrl+C 中断正在运行的命令。\r\n")

const (
	winRMLineModeNotice     = "[NexTerm] WinRM 行模式：每条命令都在新的 shell 中运行。"
	winRMFallbackNotice     = "[NexTerm] 无法启动 PowerShell 会话，已切换为行模式（每条命令在新的 shell 中运行）：%s"
	winRMShellLimitNotice   = "[NexTerm] 远程主机的 WinRM shell 数量已达上限（MaxShellsPerUser），已切换为行模式；关闭其他 WinRM 会话后重新连接即可恢复"
	winRMExitedNotice       = "[NexTerm] PowerShell 已退出（退出码 %d），已启动新的 PowerShell，之前的变量和函数已清空"
	winRMInterruptNotice    = "[NexTerm] 命令没有响应 Ctrl+C，已结束并重新启动 PowerShell，之前的变量和函数已清空"
	winRMShellLostNotice    = "[NexTerm] PowerShell 会话已中断，已启动新的 PowerShell，之前的变量和函数已清空：%s"
	winRMCommandErrorNotice = "[NexTerm] 命令未完成：%s"
	winRMOpenTimeout        = 2 * time.Minute
)

// winRMShellOpener is implemented by the WinRM transport: it starts the
// long-lived PowerShell a terminal tab sends its lines to.
type winRMShellOpener interface {
	OpenShell(context.Context, winrm.ShellOptions) (winrm.InteractiveShell, error)
}

type winRMJob struct {
	command string
	// echoed reports that the line is already on screen after its prompt.
	echoed bool
	// result receives the outcome of an ExecLine call; nil for typed lines.
	result    chan winRMJobResult
	interrupt func()
	canceled  bool
	// atPrompt reports that the job arrived while the prompt was on screen,
	// so its command replaces the line being edited there.
	atPrompt bool
}

type winRMJobResult struct {
	result base.ExecResult
	err    error
}

// winRMConsole is the terminal of a WinRM tab. WinRM has no PTY, so it edits
// the input line itself: typed keys are echoed, Enter submits the line, and
// lines typed while a command runs wait their turn. Commands run in one
// persistent PowerShell per tab; when that cannot start, it falls back to
// line mode, where every command runs in a new shell through Exec.
type winRMConsole struct {
	m      *Manager
	tab    *Tab
	ctx    context.Context
	cancel context.CancelFunc
	worker sync.WaitGroup

	mu           sync.Mutex
	closed       bool
	busy         bool
	queue        []*winRMJob
	current      *winRMJob
	line         []rune
	continuation []string
	history      []string
	historyIndex int
	escape       []byte
	partial      []byte
	lastCR       bool
	cwd          string
	atLineStart  bool
	outputCR     bool
	shell        winrm.InteractiveShell
	shellOwner   *transportHandle
	lineModeOn   *transportHandle
}

func newWinRMConsole(m *Manager, tab *Tab) *winRMConsole {
	ctx, cancel := context.WithCancel(tab.ctx)
	return &winRMConsole{m: m, tab: tab, ctx: ctx, cancel: cancel, atLineStart: true}
}

// start opens the PowerShell in the background and shows the first prompt;
// input typed meanwhile waits for it.
func (c *winRMConsole) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.busy {
		return
	}
	c.busy = true
	c.worker.Add(1)
	go c.work(true)
}

func (c *winRMConsole) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	shell := c.shell
	c.shell, c.shellOwner = nil, nil
	queued := c.queue
	c.queue = nil
	c.mu.Unlock()
	c.cancel()
	for _, job := range queued {
		job.finish(base.ExecResult{}, ErrTabClosed)
	}
	if shell != nil {
		_ = shell.Close()
	}
	c.worker.Wait()
}

func (j *winRMJob) finish(result base.ExecResult, err error) {
	if j.result != nil {
		j.result <- winRMJobResult{result: result, err: err}
	}
}

// exec runs a command for ExecLine as if it were typed, and returns when it
// finished.
func (c *winRMConsole) exec(ctx context.Context, command string) (base.ExecResult, error) {
	job := &winRMJob{command: command, result: make(chan winRMJobResult, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return base.ExecResult{}, ErrTabClosed
	}
	c.enqueueLocked(job)
	c.mu.Unlock()
	select {
	case outcome := <-job.result:
		return outcome.result, outcome.err
	case <-ctx.Done():
		c.cancelJob(job)
		return base.ExecResult{}, ctx.Err()
	}
}

func (c *winRMConsole) cancelJob(job *winRMJob) {
	c.mu.Lock()
	for index, queued := range c.queue {
		if queued == job {
			c.queue = append(c.queue[:index], c.queue[index+1:]...)
			c.mu.Unlock()
			job.finish(base.ExecResult{}, context.Canceled)
			return
		}
	}
	var interrupt func()
	if c.current == job && !job.canceled {
		job.canceled = true
		interrupt = job.interrupt
	}
	c.mu.Unlock()
	if interrupt != nil {
		interrupt()
	}
}

func (c *winRMConsole) enqueueLocked(job *winRMJob) {
	c.queue = append(c.queue, job)
	if !c.busy {
		job.atPrompt = !job.echoed
		c.busy = true
		c.worker.Add(1)
		go c.work(false)
	}
}

func (c *winRMConsole) work(startup bool) {
	defer c.worker.Done()
	if startup {
		_, _, _ = c.ensureShell()
	}
	for {
		c.mu.Lock()
		if c.closed {
			queued := c.queue
			c.queue, c.current, c.busy = nil, nil, false
			c.mu.Unlock()
			for _, job := range queued {
				job.finish(base.ExecResult{}, ErrTabClosed)
			}
			return
		}
		if len(c.queue) == 0 {
			c.current, c.busy = nil, false
			c.promptLocked(string(c.line))
			c.mu.Unlock()
			return
		}
		job := c.queue[0]
		c.queue = c.queue[1:]
		c.current = job
		switch {
		case job.atPrompt:
			c.outputCR = false
			c.feedLocked("\r\x1b[K"+c.promptTextLocked()+displayCommand(job.command)+"\r\n", true)
		case !job.echoed:
			c.promptLocked(displayCommand(job.command) + "\r\n")
		}
		c.mu.Unlock()
		started := time.Now()
		result, err := c.execute(job)
		result.Duration = time.Since(started)
		c.m.recordExecCommand(context.WithoutCancel(c.ctx), c.tab, job.command, result)
		job.finish(result, err)
	}
}

// execute runs one command in the persistent PowerShell, or through Exec in
// line mode, streaming its output to the terminal.
func (c *winRMConsole) execute(job *winRMJob) (base.ExecResult, error) {
	shell, transport, err := c.ensureShell()
	if err != nil {
		if !errors.Is(err, ErrTabClosed) {
			c.notice("\x1b[31m", fmt.Sprintf(winRMCommandErrorNotice, err.Error()))
		}
		return base.ExecResult{}, err
	}
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	if shell != nil {
		c.setInterrupt(job, func() { _ = shell.Interrupt(context.WithoutCancel(ctx)) })
		result, runErr := shell.Run(ctx, job.command)
		c.mu.Lock()
		if result.CWD != "" {
			c.cwd = result.CWD
		}
		c.mu.Unlock()
		code := result.ExitCode
		if runErr != nil {
			var exit *winrm.ShellExitError
			if errors.As(runErr, &exit) {
				code = exit.Code
			} else {
				code = 1
			}
			select {
			case <-shell.Done():
				// Report the end and start the next PowerShell right away, so
				// the prompt that follows belongs to a working session.
				_, _, _ = c.ensureShell()
				if errors.As(runErr, &exit) {
					runErr = nil
				}
			default:
			}
		}
		return base.ExecResult{ExitCode: &code}, runErr
	}
	c.setInterrupt(job, cancel)
	result, execErr := transport.Exec(ctx, job.command, base.ExecOptions{ExpectedGeneration: transport.Generation()})
	c.mu.Lock()
	if result.Stdout != "" {
		c.outputLocked(false, result.Stdout)
	}
	if result.Stderr != "" {
		c.outputLocked(true, result.Stderr)
	}
	interrupted := job.canceled
	c.mu.Unlock()
	c.followTransportCWD(transport)
	if execErr != nil && !interrupted && !errors.Is(execErr, ErrTabClosed) && c.ctx.Err() == nil {
		c.notice("\x1b[31m", fmt.Sprintf(winRMCommandErrorNotice, execErr.Error()))
	}
	return result, execErr
}

func (c *winRMConsole) setInterrupt(job *winRMJob, interrupt func()) {
	c.mu.Lock()
	job.interrupt = interrupt
	canceled := job.canceled
	c.mu.Unlock()
	if canceled {
		interrupt()
	}
}

func (c *winRMConsole) currentTransport() *transportHandle {
	session := c.tab.session
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.status != StatusConnected || session.transport == nil {
		return nil
	}
	return session.transport
}

// ensureShell returns the PowerShell bound to the session's current
// transport, replacing one that ended or belongs to an earlier connection. A
// nil shell with a nil error means line mode.
func (c *winRMConsole) ensureShell() (winrm.InteractiveShell, *transportHandle, error) {
	transport := c.currentTransport()
	if transport == nil {
		return nil, nil, ErrDisconnected
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrTabClosed
	}
	shell, owner := c.shell, c.shellOwner
	lineMode := c.lineModeOn == transport
	c.mu.Unlock()
	ended := ""
	if shell != nil {
		select {
		case <-shell.Done():
			if owner == transport {
				ended = shellEndedNotice(shell.Err())
			}
		default:
			if owner == transport {
				return shell, transport, nil
			}
		}
		c.mu.Lock()
		if c.shell == shell {
			c.shell, c.shellOwner = nil, nil
		}
		c.mu.Unlock()
		_ = shell.Close()
	}
	if lineMode {
		c.followTransportCWD(transport)
		return nil, transport, nil
	}
	opener, ok := transport.Transport.(winRMShellOpener)
	if !ok {
		c.setLineMode(transport)
		c.notice("\x1b[33m", winRMLineModeNotice)
		c.followTransportCWD(transport)
		return nil, transport, nil
	}
	ctx, cancel := context.WithTimeout(c.ctx, winRMOpenTimeout)
	opened, err := opener.OpenShell(ctx, winrm.ShellOptions{Output: c.output})
	cancel()
	if err != nil {
		if c.ctx.Err() != nil {
			return nil, nil, ErrTabClosed
		}
		c.m.logger.Warn("WinRM persistent PowerShell unavailable; using line mode", "session", c.tab.SessionID, "tab", c.tab.ID, "error", err)
		c.setLineMode(transport)
		switch {
		case errors.Is(err, winrm.ErrShellUnsupported):
			c.notice("\x1b[33m", winRMLineModeNotice)
		case errors.Is(err, winrm.ErrShellLimit):
			c.notice("\x1b[33m", winRMShellLimitNotice)
		default:
			c.notice("\x1b[33m", fmt.Sprintf(winRMFallbackNotice, err.Error()))
		}
		c.followTransportCWD(transport)
		return nil, transport, nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = opened.Close()
		return nil, nil, ErrTabClosed
	}
	c.shell, c.shellOwner = opened, transport
	c.mu.Unlock()
	// The new PowerShell reported its location to the transport while it
	// initialized.
	c.followTransportCWD(transport)
	if ended != "" {
		c.notice("\x1b[33m", ended)
	}
	return opened, transport, nil
}

func shellEndedNotice(err error) string {
	var exit *winrm.ShellExitError
	switch {
	case errors.As(err, &exit):
		return fmt.Sprintf(winRMExitedNotice, exit.Code)
	case errors.Is(err, winrm.ErrInterruptTimeout):
		return winRMInterruptNotice
	case err == nil:
		return fmt.Sprintf(winRMShellLostNotice, "")
	default:
		return fmt.Sprintf(winRMShellLostNotice, err.Error())
	}
}

// followTransportCWD takes the transport's working directory, which line mode
// and a starting PowerShell report there.
func (c *winRMConsole) followTransportCWD(transport *transportHandle) {
	locator, ok := transport.Transport.(interface{ CWD() string })
	if !ok {
		return
	}
	cwd := locator.CWD()
	c.mu.Lock()
	if cwd != "" {
		c.cwd = cwd
	}
	c.mu.Unlock()
}

func (c *winRMConsole) setLineMode(transport *transportHandle) {
	c.mu.Lock()
	c.lineModeOn = transport
	c.mu.Unlock()
}

// notice prints one line of NexTerm status on a line of its own.
func (c *winRMConsole) notice(color, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := ""
	if !c.atLineStart {
		prefix = "\r\n"
	}
	c.feedLocked(prefix+color+text+"\x1b[0m\r\n", true)
}

func (c *winRMConsole) output(stderr bool, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputLocked(stderr, text)
}

// outputLocked shows command output, turning bare LF into CRLF since no PTY
// does it, and colors stderr.
func (c *winRMConsole) outputLocked(stderr bool, text string) {
	if text == "" || c.closed {
		return
	}
	var normalized strings.Builder
	for index := 0; index < len(text); index++ {
		b := text[index]
		if b == '\n' && !c.outputCR {
			normalized.WriteByte('\r')
		}
		normalized.WriteByte(b)
		c.outputCR = b == '\r'
	}
	display := normalized.String()
	if stderr {
		display = "\x1b[31m" + display + "\x1b[0m"
	}
	c.feedLocked(display, text[len(text)-1] == '\n')
}

func (c *winRMConsole) feedLocked(data string, endsLine bool) {
	if data == "" {
		return
	}
	c.atLineStart = endsLine
	c.tab.mu.Lock()
	generation := c.tab.generation
	c.tab.mu.Unlock()
	_ = c.m.feed(c.ctx, c.tab, generation, []byte(data))
}

func (c *winRMConsole) promptTextLocked() string {
	if len(c.continuation) > 0 {
		return ">> "
	}
	if c.cwd == "" {
		return "PS> "
	}
	return "PS " + c.cwd + "> "
}

// promptLocked starts a fresh prompt line followed by text.
func (c *winRMConsole) promptLocked(text string) {
	prefix := ""
	if !c.atLineStart {
		prefix = "\r\n"
	}
	c.outputCR = false
	display := prefix + c.promptTextLocked() + text
	c.feedLocked(display, strings.HasSuffix(display, "\n"))
}

func displayCommand(command string) string {
	return strings.ReplaceAll(strings.ReplaceAll(command, "\r\n", "\n"), "\n", "\r\n>> ")
}

// input edits the line with the typed bytes. While a command runs, typed text
// is kept without echo and shown when its turn comes.
func (c *winRMConsole) input(data []byte) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	var echo strings.Builder
	var interrupts []func()
	data = append(c.partial, data...)
	c.partial = nil
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			c.partial = append(c.partial, data...)
			break
		}
		r, size := utf8.DecodeRune(data)
		raw := data[:size]
		data = data[size:]
		if len(c.escape) > 0 || r == 0x1b {
			c.escape = append(c.escape, raw...)
			if sequence, done := escapeComplete(c.escape); done {
				c.escape = nil
				c.keySequenceLocked(sequence, &echo)
			}
			continue
		}
		lastCR := c.lastCR
		c.lastCR = r == '\r'
		switch {
		case r == '\n' && lastCR:
		case r == '\r' || r == '\n':
			c.enterLocked(&echo)
		case r == 0x03:
			if interrupt := c.interruptLocked(&echo); interrupt != nil {
				interrupts = append(interrupts, interrupt)
			}
		case r == 0x7f || r == 0x08:
			if len(c.line) > 0 {
				last := c.line[len(c.line)-1]
				c.line = c.line[:len(c.line)-1]
				if !c.busy {
					erase := strings.Repeat("\b", runeCells(last))
					echo.WriteString(erase + strings.Repeat(" ", runeCells(last)) + erase)
				}
			}
		case r == 0x15:
			c.line = nil
			if !c.busy {
				echo.WriteString("\r\x1b[K" + c.promptTextLocked())
			}
		case r == 0x0c:
			if !c.busy {
				echo.WriteString("\x1b[H\x1b[2J" + c.promptTextLocked() + string(c.line))
			}
		case r == utf8.RuneError && size == 1, r < 0x20:
		default:
			c.line = append(c.line, r)
			if !c.busy {
				echo.WriteString(string(r))
			}
		}
	}
	if echo.Len() > 0 {
		text := echo.String()
		c.feedLocked(text, strings.HasSuffix(text, "\n"))
	}
	c.mu.Unlock()
	for _, interrupt := range interrupts {
		interrupt()
	}
}

func (c *winRMConsole) enterLocked(echo *strings.Builder) {
	text := string(c.line)
	c.line = nil
	c.historyIndex = len(c.history)
	idle := !c.busy
	if idle {
		echo.WriteString("\r\n")
	}
	if text != "" && winRMIncomplete(strings.Join(append(append([]string(nil), c.continuation...), text), "\n")) {
		c.continuation = append(c.continuation, text)
		if idle {
			echo.WriteString(c.promptTextLocked())
		}
		return
	}
	command := strings.Join(append(c.continuation, text), "\n")
	c.continuation = nil
	if strings.TrimSpace(command) == "" {
		if idle {
			echo.WriteString(c.promptTextLocked())
		}
		return
	}
	if len(c.history) == 0 || c.history[len(c.history)-1] != command {
		c.history = append(c.history, command)
	}
	c.historyIndex = len(c.history)
	if idle && echo.Len() > 0 {
		// The echo of this line must reach the screen before the worker
		// prints the command's output.
		text := echo.String()
		echo.Reset()
		c.feedLocked(text, strings.HasSuffix(text, "\n"))
	}
	c.enqueueLocked(&winRMJob{command: command, echoed: idle})
}

// interruptLocked handles Ctrl+C: at the prompt it abandons the line; while
// a command runs it also drops the lines typed ahead and interrupts it.
func (c *winRMConsole) interruptLocked(echo *strings.Builder) func() {
	c.line, c.continuation = nil, nil
	if !c.busy {
		echo.WriteString("^C\r\n" + c.promptTextLocked())
		return nil
	}
	echo.WriteString("^C")
	dropped := c.queue
	c.queue = nil
	for _, job := range dropped {
		job.finish(base.ExecResult{}, context.Canceled)
	}
	job := c.current
	if job == nil || job.canceled {
		return nil
	}
	job.canceled = true
	return job.interrupt
}

func (c *winRMConsole) keySequenceLocked(sequence string, echo *strings.Builder) {
	if c.busy || len(c.history) == 0 {
		return
	}
	switch sequence {
	case "\x1b[A", "\x1bOA":
		if c.historyIndex > 0 {
			c.historyIndex--
		}
	case "\x1b[B", "\x1bOB":
		if c.historyIndex < len(c.history) {
			c.historyIndex++
		}
	default:
		return
	}
	entry := ""
	if c.historyIndex < len(c.history) {
		entry = c.history[c.historyIndex]
	}
	// A multi-line history entry is recalled as one line.
	c.line = []rune(strings.ReplaceAll(entry, "\n", "; "))
	echo.WriteString("\r\x1b[K" + c.promptTextLocked() + string(c.line))
}

// escapeComplete reports whether an escape sequence (CSI, SS3 or an Alt
// chord) is complete.
func escapeComplete(sequence []byte) (string, bool) {
	if len(sequence) < 2 {
		return "", false
	}
	switch sequence[1] {
	case '[':
		last := sequence[len(sequence)-1]
		if len(sequence) > 2 && last >= 0x40 && last <= 0x7e {
			return string(sequence), true
		}
		return "", len(sequence) > 32
	case 'O':
		return string(sequence), len(sequence) >= 3
	default:
		return string(sequence), true
	}
}

func runeCells(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}

// winRMIncomplete approximates PowerShell's continuation rule: an open
// bracket, quote or here-string, or a line ending in a backtick or a pipe,
// asks for more lines; a blank line always ends the input.
func winRMIncomplete(text string) bool {
	trimmed := strings.TrimRight(text, " \t")
	if strings.HasSuffix(trimmed, "`") || strings.HasSuffix(trimmed, "|") {
		return true
	}
	depth := 0
	var quote, here byte
	for _, line := range strings.Split(text, "\n") {
		if here != 0 {
			if strings.HasPrefix(line, string(here)+"@") {
				here = 0
			}
			continue
		}
	scan:
		for index := 0; index < len(line); index++ {
			b := line[index]
			if quote != 0 {
				switch {
				case b == '`' && quote == '"':
					index++
				case b == quote:
					quote = 0
				}
				continue
			}
			switch b {
			case '#':
				break scan
			case '`':
				index++
			case '\'', '"':
				if index > 0 && line[index-1] == '@' && strings.TrimSpace(line[index+1:]) == "" {
					here = b
					break scan
				}
				quote = b
			case '{', '(', '[':
				depth++
			case '}', ')', ']':
				depth--
			}
		}
	}
	return depth > 0 || quote != 0 || here != 0
}
