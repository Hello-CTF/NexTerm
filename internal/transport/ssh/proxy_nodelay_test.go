//go:build darwin || linux

package ssh

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"testing"
)

func tcpNoDelay(t *testing.T, conn net.Conn) int {
	t.Helper()
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatalf("connection is %T, want *net.TCPConn", conn)
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	value := -1
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		var err error
		value, err = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
		controlErr = err
	}); err != nil {
		t.Fatal(err)
	}
	if controlErr != nil {
		t.Fatal(controlErr)
	}
	return value
}

func TestDialSSHDirectSetsNoDelay(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialSSH(context.Background(), Config{}, "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcpNoDelay(t, conn) == 0 {
		t.Fatal("TCP_NODELAY is off, want on")
	}
	server := <-accepted
	defer server.Close()
}

func TestSetNoDelayRestoresDisabledFlag(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	setNoDelay(left)
	setNoDelay(right)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = conn.Read(make([]byte, 1))
		}
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).SetNoDelay(false); err != nil {
		t.Fatal(err)
	}
	if got := tcpNoDelay(t, conn); got != 0 {
		t.Fatalf("TCP_NODELAY = %d after disabling, want 0", got)
	}
	setNoDelay(conn)
	if tcpNoDelay(t, conn) == 0 {
		t.Fatal("TCP_NODELAY is off after setNoDelay, want on")
	}
}
