package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
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
	cmd       *exec.Cmd
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
		case <-time.After(proxyCommandCloseTimeout):
			_ = killProxyCommand(c.cmd)
			<-c.waitDone
		}
		_ = c.stdout.Close()
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
	shell := proxyCommandShell(expandProxyCommand(command, host, port))
	cmd := exec.Command(shell[0], shell[1:]...)
	configureProxyProc(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy command stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("proxy command stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start proxy command: %w", err)
	}
	conn := &proxyCommandConn{cmd: cmd, stdin: stdin, stdout: stdout, waitDone: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(conn.waitDone)
	}()
	return conn, nil
}
