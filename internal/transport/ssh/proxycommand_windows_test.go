//go:build windows

package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func proxyHelperCommand() string {
	return fmt.Sprintf("set NEXTERM_SSH_PROXY_HELPER=1 && \"%s\" -test.run=^TestSSHProxyCommandHelper$ %%h %%p", os.Args[0])
}

func TestProxyCommandWindowsCloseKillsChildTree(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	host, port := splitAddress(t, listener.Addr().String())
	command := fmt.Sprintf("set NEXTERM_SSH_PROXY_TREE=1 && \"%s\" -test.run=^TestSSHProxyCommandTreeHelper$ %s %d", os.Args[0], host, port)
	conn, err := dialProxyCommand(context.Background(), command, "unused.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn := conn.(*proxyCommandConn)
	var grandchildConn net.Conn
	select {
	case grandchildConn = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("grandchild helper did not connect")
	}
	if err := proxyConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxyConn.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy command shell was not reaped on close")
	}
	_ = grandchildConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	one := make([]byte, 1)
	if _, err := grandchildConn.Read(one); err == nil {
		t.Fatal("grandchild connection still open after close; the proxy process tree survived")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("grandchild connection still open after close; the proxy process tree survived")
	}
}

func TestSSHProxyCommandTreeHelper(t *testing.T) {
	if os.Getenv("NEXTERM_SSH_PROXY_TREE") != "1" {
		t.Skip("helper process for the Windows proxy command tree test")
	}
	args := os.Args
	host, port := args[len(args)-2], args[len(args)-1]
	child := exec.Command(os.Args[0], "-test.run=^TestSSHProxyCommandGrandchild$", host, port)
	child.Env = append(os.Environ(), "NEXTERM_SSH_PROXY_GRANDCHILD=1")
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = child.Wait()
	os.Exit(0)
}

func TestSSHProxyCommandGrandchild(t *testing.T) {
	if os.Getenv("NEXTERM_SSH_PROXY_GRANDCHILD") != "1" {
		t.Skip("helper process for the Windows proxy command tree test")
	}
	args := os.Args
	host, port := args[len(args)-2], args[len(args)-1]
	conn, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()
	_, _ = io.Copy(io.Discard, conn)
	os.Exit(0)
}
