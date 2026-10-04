package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const proxyCommandCloseTimeout = 2 * time.Second

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
	waitDone  chan struct{}
	closeOnce sync.Once
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
			_ = c.proc.kill()
			<-c.waitDone
		}
		_ = c.stdout.Close()
		_ = c.proc.close()
	})
	return nil
}

type proxyCommandAddr string

func (a proxyCommandAddr) Network() string { return "proxycommand" }
func (a proxyCommandAddr) String() string  { return string(a) }

func dialProxyCommand(ctx context.Context, command, host string, port int) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	proc, stdin, stdout, err := startProxyCommand(expandProxyCommand(command, host, port))
	if err != nil {
		return nil, err
	}
	conn := &proxyCommandConn{proc: proc, stdin: stdin, stdout: stdout, waitDone: make(chan struct{})}
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
