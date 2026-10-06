package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestListenRemoteBindsRelaysAndReleasesPort(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})

	listener, err := client.ListenRemote(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		t.Fatalf("allocated port = %q, %v", portText, err)
	}

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	origin, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer origin.Close()

	var forwarded net.Conn
	select {
	case forwarded = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("remote forwarded connection was not accepted")
	}
	if forwarded.RemoteAddr().String() != origin.LocalAddr().String() {
		t.Fatalf("forwarded remote addr = %q, want origin %q", forwarded.RemoteAddr(), origin.LocalAddr())
	}

	if err := origin.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := origin.Write([]byte("remote-echo")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("remote-echo"))
	if _, err := io.ReadFull(forwarded, buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := forwarded.Write(buffer); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(origin, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "remote-echo" {
		t.Fatalf("relayed payload = %q", buffer)
	}
	forwarded.Close()

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); err == nil {
		t.Fatal("Accept succeeded after Close")
	}
	if conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("server-side port still open after listener Close")
	}
}

func TestListenRemotePortReleasedWhenClientDisconnects(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})

	listener, err := client.ListenRemote(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("server-side remote forward port survived client disconnect")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestListenRemoteValidationAndLifecycle(t *testing.T) {
	server := newTestSSHServer(t, nil)
	client := connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})

	if _, err := client.ListenRemote(context.Background(), "udp", "127.0.0.1:0"); err == nil {
		t.Fatal("udp remote listen was not rejected")
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if _, err := client.ListenRemote(context.Background(), "tcp", taken.Addr().String()); err == nil {
		t.Fatal("duplicate remote listen was not rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ListenRemote(ctx, "tcp", "127.0.0.1:0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled listen = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListenRemote(context.Background(), "tcp", "127.0.0.1:0"); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("listen after close = %v", err)
	}
}
