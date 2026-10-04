//go:build !windows

package ssh

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func proxyHelperCommand() string {
	return fmt.Sprintf("NEXTERM_SSH_PROXY_HELPER=1 %q -test.run=^TestSSHProxyCommandHelper$ %%h %%p", os.Args[0])
}

func TestProxyCommandCloseReapsProcess(t *testing.T) {
	conn, err := dialProxyCommand(context.Background(), "cat", "example.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn := conn.(*proxyCommandConn)
	if _, err := proxyConn.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 7)
	if _, err := io.ReadFull(proxyConn, echo); err != nil || string(echo) != "payload" {
		t.Fatalf("proxy command echo = %q, %v", echo, err)
	}
	if err := proxyConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxyConn.waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy command process was not reaped on close")
	}
	if proxyConn.proc.cmd.ProcessState == nil {
		t.Fatal("proxy command Wait did not record a process state")
	}
	if err := proxyConn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProxyCommandCloseKillsProcessGroup(t *testing.T) {
	conn, err := dialProxyCommand(context.Background(), "sleep 30 & wait", "example.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn := conn.(*proxyCommandConn)
	if err := proxyConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxyConn.waitDone:
	case <-time.After(proxyCommandCloseTimeout + 3*time.Second):
		t.Fatal("closing the connection did not kill the proxy command process group")
	}
	if proxyConn.proc.cmd.ProcessState == nil {
		t.Fatal("proxy command Wait did not record a process state")
	}
}
