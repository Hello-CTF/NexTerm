package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	proxyCommandCloseTimeout = 2 * time.Second
	// proxyCommandStderrWait bounds how long reaping waits for stderr after the
	// process exits, for when a background child keeps the pipe open.
	proxyCommandStderrWait = time.Second
	proxyCommandStderrTail = 4 << 10
)

func expandProxyCommand(command, host string, port int) string {
	return strings.NewReplacer(
		"%h", host,
		"%p", strconv.Itoa(port),
		"%%", "%",
	).Replace(command)
}

type proxyCommandConn struct {
	proc      *proxyCommandProcess
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    *tailBuffer
	waitDone  chan struct{}
	closeOnce sync.Once
	killed    atomic.Bool
}

func (c *proxyCommandConn) Read(b []byte) (int, error)  { return c.stdout.Read(b) }
func (c *proxyCommandConn) Write(b []byte) (int, error) { return c.stdin.Write(b) }
func (c *proxyCommandConn) LocalAddr() net.Addr         { return proxyCommandAddr("local") }
func (c *proxyCommandConn) RemoteAddr() net.Addr        { return proxyCommandAddr("remote") }

func (c *proxyCommandConn) SetDeadline(time.Time) error      { return nil }
func (c *proxyCommandConn) SetReadDeadline(time.Time) error  { return nil }
func (c *proxyCommandConn) SetWriteDeadline(time.Time) error { return nil }

func (c *proxyCommandConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		select {
		case <-c.waitDone:
			_ = c.proc.kill()
		case <-time.After(proxyCommandCloseTimeout):
			c.killed.Store(true)
			_ = c.proc.kill()
			<-c.waitDone
		}
		_ = c.stdout.Close()
		_ = c.proc.close()
	})
	return nil
}

// exitError wraps err with how the proxy command ended when it exited on its
// own with a failure status or error output. It returns err unchanged while
// the process runs or after Close had to kill it.
func (c *proxyCommandConn) exitError(err error) error {
	select {
	case <-c.waitDone:
	default:
		return err
	}
	if c.killed.Load() {
		return err
	}
	code := c.proc.exitCode()
	stderr := c.stderr.String()
	if code == 0 && stderr == "" {
		return err
	}
	return &proxyCommandExitError{code: code, stderr: stderr, err: err}
}

type proxyCommandExitError struct {
	code   int
	stderr string
	err    error
}

func (e *proxyCommandExitError) Error() string {
	status := "proxy command terminated abnormally"
	if e.code >= 0 {
		status = fmt.Sprintf("proxy command exited with status %d", e.code)
	}
	if e.stderr != "" {
		status += " (" + e.stderr + ")"
	}
	return status + ": " + e.err.Error()
}

func (e *proxyCommandExitError) Unwrap() error { return e.err }

func (e *proxyCommandExitError) userDetail() string {
	detail := "。代理命令异常结束"
	if e.code >= 0 {
		detail = fmt.Sprintf("。代理命令以退出码 %d 结束", e.code)
	}
	if e.stderr != "" {
		detail += fmt.Sprintf("，错误输出：%s", e.stderr)
	}
	return detail
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if overflow := len(b.buf) + len(p) - b.limit; overflow > 0 {
		b.buf = b.buf[:copy(b.buf, b.buf[overflow:])]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(strings.ToValidUTF8(string(b.buf), ""))
}

type proxyCommandAddr string

func (a proxyCommandAddr) Network() string { return "proxycommand" }
func (a proxyCommandAddr) String() string  { return string(a) }

func dialProxyCommand(ctx context.Context, command, host string, port int) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stderr := newTailBuffer(proxyCommandStderrTail)
	proc, stdin, stdout, err := startProxyCommand(expandProxyCommand(command, host, port), stderr)
	if err != nil {
		return nil, err
	}
	conn := &proxyCommandConn{proc: proc, stdin: stdin, stdout: stdout, stderr: stderr, waitDone: make(chan struct{})}
	go func() {
		_ = proc.wait()
		close(conn.waitDone)
	}()
	return conn, nil
}

func proxyCommandStartError(err error) error {
	return fmt.Errorf("start proxy command: %w", err)
}

func proxyCommandLine(executable, command string) string {
	return "\"" + executable + "\" /C " + command
}
