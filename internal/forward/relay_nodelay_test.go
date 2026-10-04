//go:build darwin || linux

package forward

import (
	"net"
	"syscall"
	"testing"
	"time"
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

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
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
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return client, <-accepted
}

func TestRelayAppliesNoDelayToBothEnds(t *testing.T) {
	left, right := tcpPair(t)
	defer left.Close()
	defer right.Close()
	if err := left.(*net.TCPConn).SetNoDelay(false); err != nil {
		t.Fatal(err)
	}
	if err := right.(*net.TCPConn).SetNoDelay(false); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		relay(left, right)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for tcpNoDelay(t, left) == 0 || tcpNoDelay(t, right) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("relay did not set TCP_NODELAY on both ends")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = left.Close()
	_ = right.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not unwind after close")
	}
}

func TestSetNoDelayIgnoresNonTCP(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	setNoDelay(left)
	setNoDelay(right)
}
