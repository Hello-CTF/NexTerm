package session

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/telnet"
)

// readTelnetPeer reads from the server side of a Telnet connection until want appears.
func readTelnetPeer(t *testing.T, conn net.Conn, received *bytes.Buffer, want []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	for !bytes.Contains(received.Bytes(), want) {
		n, err := conn.Read(buf)
		received.Write(buf[:n])
		if err != nil {
			t.Fatalf("server read %q, want %q: %v", received.Bytes(), want, err)
		}
	}
}

func TestTelnetTabIsTemporaryAndEndsWithItsConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	connector := ConnectorFunc(func(ctx context.Context, _ Asset, _ uint64) (base.Transport, error) {
		return telnet.Connect(ctx, telnet.Config{Host: address.IP.String(), Port: address.Port})
	})
	manager := NewManager(Config{Connector: connector})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "switch", Kind: KindTelnet})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	var peer net.Conn
	select {
	case peer = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("no telnet connection")
	}
	t.Cleanup(func() { _ = peer.Close() })
	var received bytes.Buffer

	if _, err := peer.Write([]byte("\xff\xfd\x1fUsername: ")); err != nil {
		t.Fatal(err)
	}
	readTelnetPeer(t, peer, &received, []byte{255, 250, 31, 0, 80, 0, 24, 255, 240})
	waitFor(t, func() bool { return bytes.Contains(tab.terminal.Dump(1<<20), []byte("Username: ")) })

	if err := manager.Write(context.Background(), tab.ID, "client-a", []byte("admin\r")); err != nil {
		t.Fatal(err)
	}
	readTelnetPeer(t, peer, &received, []byte("admin\r\x00"))
	if err := manager.Resize(context.Background(), tab.ID, "client-a", 120, 40); err != nil {
		t.Fatal(err)
	}
	readTelnetPeer(t, peer, &received, []byte{255, 250, 31, 0, 120, 0, 40, 255, 240})

	tab.mu.Lock()
	info := tab.infoLocked()
	tab.mu.Unlock()
	if info.Retainable || info.Durable {
		t.Fatalf("telnet tab info = %+v, want a temporary terminal", info)
	}

	_ = peer.Close()
	waitFor(t, func() bool {
		tab.mu.Lock()
		defer tab.mu.Unlock()
		return tab.exited
	})
}
