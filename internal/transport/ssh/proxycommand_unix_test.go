//go:build !windows

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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

func TestProxyCommandFailureReportsExitStatusAndStderr(t *testing.T) {
	cfg := Config{
		Host:              "bastion.example",
		Port:              22,
		User:              "test",
		Auth:              AuthConfig{Method: AuthPassword, Password: "secret"},
		HostKeys:          NewMemoryHostKeyStore(),
		AutoAcceptUnknown: true,
		ConnectTimeout:    5 * time.Second,
		KeepAliveInterval: -1,
		ProxyCommand:      "sh -c 'echo boom >&2; exit 3'",
	}
	_, err := Connect(context.Background(), cfg)
	var connectErr *ConnectError
	if !errors.As(err, &connectErr) {
		t.Fatalf("error = %T %v, want *ConnectError", err, err)
	}
	if connectErr.Kind != ErrorKindProxy {
		t.Fatalf("kind = %q, want %q (%v)", connectErr.Kind, ErrorKindProxy, err)
	}
	for _, want := range []string{"exited with status 3", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err.Error(), want)
		}
	}
	message := connectErr.UserMessage()
	for _, want := range []string{"退出码 3", "boom"} {
		if !strings.Contains(message, want) {
			t.Fatalf("user message %q does not contain %q", message, want)
		}
	}
}

func TestProxyCommandStderrKeepsBoundedTail(t *testing.T) {
	conn, err := dialProxyCommand(context.Background(), "head -c 10000 /dev/zero | tr '\\0' a >&2; printf tail >&2; exit 1", "example.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn := conn.(*proxyCommandConn)
	if err := proxyConn.Close(); err != nil {
		t.Fatal(err)
	}
	stderr := proxyConn.stderr.String()
	if len(stderr) != proxyCommandStderrTail || !strings.HasSuffix(stderr, "tail") {
		t.Fatalf("stderr tail has %d bytes ending %q", len(stderr), stderr[max(0, len(stderr)-8):])
	}
	var exitErr *proxyCommandExitError
	if !errors.As(proxyConn.exitError(io.EOF), &exitErr) || exitErr.code != 1 {
		t.Fatalf("exit error = %v", proxyConn.exitError(io.EOF))
	}
}

func TestProxyCommandCleanExitAddsNoDetail(t *testing.T) {
	conn, err := dialProxyCommand(context.Background(), "true", "example.test", 22)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn := conn.(*proxyCommandConn)
	if err := proxyConn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := proxyConn.exitError(io.EOF); got != io.EOF {
		t.Fatalf("exit error = %v, want io.EOF unchanged", got)
	}
}
