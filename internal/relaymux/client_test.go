package relaymux

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// echoAgent accepts every session on sessions and echoes every accepted device stream; links are reported on
// the returned channel so a test can end them.
func echoAgent(t testing.TB, sessions chan Session) chan *Link {
	t.Helper()
	links := make(chan *Link, 16)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case session := <-sessions:
				go func() {
					pending, err := AcceptHandshake(ctx, session)
					if err != nil {
						return
					}
					link, err := pending.Accept(Reply{})
					if err != nil {
						return
					}
					links <- link
					_ = link.ServeStreams(ctx, func(_ context.Context, incoming *IncomingStream) {
						conn, err := incoming.Accept(nil)
						if err != nil {
							return
						}
						_, _ = io.Copy(conn, conn)
						_ = CloseWrite(conn)
					})
				}()
			}
		}
	}()
	return links
}

// The client shares one dial between concurrent callers, keeps its link for later streams and redials once the
// link ended.
func TestClientKeepsOneLinkAndRedials(t *testing.T) {
	server := newWSServer(t)
	links := echoAgent(t, server.sessions)
	var dials atomic.Int32
	client := NewClient(func(ctx context.Context) (*Link, error) {
		dials.Add(1)
		return Dial(ctx, DialConfig{WSURL: server.url(), Backoff: NewQUICBackoff(time.Minute, nil)})
	})
	t.Cleanup(func() { _ = client.Close() })
	roundTrip := func() {
		conn, _, err := client.OpenDeviceStream(context.Background(), StreamHeader{Purpose: "terminal"})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("hi"))
		_ = CloseWrite(conn)
		if got, _ := io.ReadAll(conn); string(got) != "hi" {
			t.Errorf("echo = %q", got)
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			roundTrip()
		}()
	}
	wg.Wait()
	if n := dials.Load(); n != 1 {
		t.Fatalf("dials = %d, want one shared link", n)
	}
	first := <-links
	_ = first.Close()
	<-first.Done()
	current, _ := client.Current()
	if current != nil {
		<-current.Done()
	}
	roundTrip()
	if n := dials.Load(); n != 2 {
		t.Fatalf("dials = %d after the link ended, want 2", n)
	}
}

// A refusal comes back as is and does not make the client redial.
func TestClientReturnsStreamRefusal(t *testing.T) {
	server := newWSServer(t)
	go func() {
		for session := range server.sessions {
			go func() {
				pending, err := AcceptHandshake(context.Background(), session)
				if err != nil {
					return
				}
				link, err := pending.Accept(Reply{})
				if err != nil {
					return
				}
				_ = link.ServeStreams(context.Background(), func(_ context.Context, incoming *IncomingStream) {
					incoming.Refuse(Reply{Status: http.StatusServiceUnavailable, Code: "disconnected", Message: "设备不在线"})
				})
			}()
		}
	}()
	var dials atomic.Int32
	client := NewClient(func(ctx context.Context) (*Link, error) {
		dials.Add(1)
		return Dial(ctx, DialConfig{WSURL: server.url(), Backoff: NewQUICBackoff(time.Minute, nil)})
	})
	t.Cleanup(func() { _ = client.Close() })
	_, _, err := client.OpenDeviceStream(context.Background(), StreamHeader{Purpose: "terminal", DeviceID: "d"})
	reply, ok := Refused(err)
	if !ok || reply.Status != http.StatusServiceUnavailable || reply.Code != "disconnected" || reply.Message != "设备不在线" {
		t.Fatalf("open = %v", err)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("dials = %d", n)
	}
}

func TestConfigDialConfig(t *testing.T) {
	config := Config{Enabled: true, Version: Version, WSPath: WSPath, ALPN: ALPN, Pin: testCert.Pin,
		QUIC: []Endpoint{{Host: "relay.example.com", Port: 3478}}}
	dial, err := config.DialConfig("https://relay.example.com/base/", DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dial.WSURL != "wss://relay.example.com/base/relay/ws" || len(dial.QUICAddrs) != 1 || dial.QUICAddrs[0] != "relay.example.com:3478" || dial.Pin != testCert.Pin {
		t.Fatalf("dial config = %+v", dial)
	}
	// A config fetched over plain HTTP or unverified TLS vouches for no QUIC server.
	for _, c := range []struct {
		base       string
		unverified bool
	}{{"http://relay.example.com", false}, {"https://relay.example.com", true}} {
		dial, err := config.DialConfig(c.base, DialOptions{UnverifiedTLS: c.unverified})
		if err != nil {
			t.Fatal(err)
		}
		if len(dial.QUICAddrs) != 0 || dial.WSURL == "" {
			t.Fatalf("%s (unverified %v): %+v", c.base, c.unverified, dial)
		}
	}
	if _, err := (Config{Enabled: false, Version: Version}).DialConfig("https://x.example", DialOptions{}); err == nil {
		t.Fatal("a disabled relay produced a dial config")
	}
}
