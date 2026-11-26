package relaymux

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/stunserver"
	"github.com/pion/stun/v4"
)

// acceptAll authenticates every session arriving on sessions with an accepting reply.
func acceptAll(t testing.TB, sessions chan Session) {
	t.Helper()
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
					<-ctx.Done()
					_ = link.Close()
				}()
			}
		}
	}()
}

// manualTimer is a head start timer the test fires.
type manualTimer struct {
	fire    chan time.Time
	started atomic.Int32
}

func newManualTimer() *manualTimer { return &manualTimer{fire: make(chan time.Time, 1)} }

func (m *manualTimer) after(time.Duration) <-chan time.Time {
	m.started.Add(1)
	return m.fire
}

func dialFor(t testing.TB, config DialConfig) *Link {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	link, err := Dial(ctx, config)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = link.Close() })
	return link
}

// With QUIC working it wins during the head start and the websocket carrier is never dialed.
func TestDialPrefersQUIC(t *testing.T) {
	quicServer, wsServer := newQUICServer(t), newWSServer(t)
	acceptAll(t, quicServer.sessions)
	acceptAll(t, wsServer.sessions)
	timer := newManualTimer()
	backoff := NewQUICBackoff(time.Minute, nil)
	link := dialFor(t, DialConfig{
		QUICAddrs: []string{quicServer.addr()}, Pin: testCert.Pin,
		WSURL: wsServer.url(), Backoff: backoff, after: timer.after,
	})
	if link.Kind() != KindQUIC {
		t.Fatalf("kind = %s, want quic", link.Kind())
	}
	if n := wsServer.upgrades.Load(); n != 0 {
		t.Fatalf("websocket upgrades = %d while QUIC won inside the head start", n)
	}
	if backoff.Blocked(wsServer.url()) {
		t.Fatal("a QUIC win left the server in backoff")
	}
}

// With UDP dropped the websocket starts when the head start ends and wins; QUIC is then suspended, so the next
// dial goes straight to the websocket without opening a UDP socket or waiting.
func TestDialFallsBackWhenUDPIsBlocked(t *testing.T) {
	quicServer, wsServer := newQUICServer(t), newWSServer(t)
	acceptAll(t, wsServer.sessions)
	var sockets atomic.Int32
	listen := func() (net.PacketConn, error) {
		sockets.Add(1)
		return newDroppingConn(), nil
	}
	timer := newManualTimer()
	backoff := NewQUICBackoff(time.Minute, nil)
	config := DialConfig{
		QUICAddrs: []string{quicServer.addr()}, Pin: testCert.Pin, QUICTimeout: time.Minute,
		WSURL: wsServer.url(), Backoff: backoff, ListenPacket: listen, after: timer.after,
	}
	type result struct {
		link *Link
		err  error
	}
	done := make(chan result, 1)
	go func() {
		link, err := Dial(context.Background(), config)
		done <- result{link, err}
	}()
	// The websocket waits for the head start: QUIC's packets vanish, so nothing else can happen first.
	for sockets.Load() == 0 || timer.started.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	select {
	case r := <-done:
		t.Fatalf("dial finished before the head start ended: %v", r.err)
	default:
	}
	if n := wsServer.upgrades.Load(); n != 0 {
		t.Fatalf("websocket dialed %d times during the head start", n)
	}
	timer.fire <- time.Now()
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { _ = r.link.Close() })
	if r.link.Kind() != KindWS {
		t.Fatalf("kind = %s, want ws", r.link.Kind())
	}
	if !backoff.Blocked(wsServer.url()) {
		t.Fatal("losing QUIC did not suspend it")
	}

	second := newManualTimer()
	config.after = second.after
	link := dialFor(t, config)
	if link.Kind() != KindWS || sockets.Load() != 1 || second.started.Load() != 0 {
		t.Fatalf("second dial: kind %s, sockets %d, head starts %d; want ws, 1, 0", link.Kind(), sockets.Load(), second.started.Load())
	}
}

// A QUIC attempt that fails outright starts the websocket at once instead of waiting out the head start.
func TestDialQUICFailureSkipsHeadStart(t *testing.T) {
	wsServer := newWSServer(t)
	acceptAll(t, wsServer.sessions)
	timer := newManualTimer()
	backoff := NewQUICBackoff(time.Minute, nil)
	link := dialFor(t, DialConfig{
		QUICAddrs: []string{"127.0.0.1:9"}, Pin: testCert.Pin,
		WSURL: wsServer.url(), Backoff: backoff, after: timer.after,
		ListenPacket: func() (net.PacketConn, error) { return nil, errors.New("udp unavailable") },
	})
	if link.Kind() != KindWS {
		t.Fatalf("kind = %s", link.Kind())
	}
	if !backoff.Blocked(wsServer.url()) {
		t.Fatal("a failed QUIC attempt did not suspend QUIC")
	}
}

// The backoff expires: after the period QUIC is tried again.
func TestQUICBackoffExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	backoff := NewQUICBackoff(time.Minute, func() time.Time { return now })
	backoff.Fail("s")
	if !backoff.Blocked("s") || backoff.Blocked("other") {
		t.Fatal("fail did not block exactly its key")
	}
	now = now.Add(time.Minute)
	if backoff.Blocked("s") {
		t.Fatal("backoff outlived its period")
	}
}

// A refused Hello is final: Dial returns the refusal instead of trying the other carrier.
func TestDialReturnsRefusal(t *testing.T) {
	quicServer, wsServer := newQUICServer(t), newWSServer(t)
	go func() {
		for session := range quicServer.sessions {
			if pending, err := AcceptHandshake(context.Background(), session); err == nil {
				pending.Refuse(Reply{Status: http.StatusUnauthorized, Code: "forbidden", Message: "会话无效或缺失，请重新登录"})
			}
		}
	}()
	timer := newManualTimer()
	_, err := Dial(context.Background(), DialConfig{
		QUICAddrs: []string{quicServer.addr()}, Pin: testCert.Pin,
		WSURL: wsServer.url(), Backoff: NewQUICBackoff(time.Minute, nil), after: timer.after,
	})
	if reply, ok := Refused(err); !ok || reply.Status != http.StatusUnauthorized {
		t.Fatalf("dial = %v, want the 401 refusal", err)
	}
	if n := wsServer.upgrades.Load(); n != 0 {
		t.Fatalf("websocket dialed %d times after a refusal", n)
	}
}

// A client never accepts a QUIC server whose key differs from the pin.
func TestDialQUICRejectsWrongPin(t *testing.T) {
	server := newQUICServer(t)
	other := testCert.Pin[:len(testCert.Pin)-2] + "AA"
	if other == testCert.Pin {
		other = testCert.Pin[:len(testCert.Pin)-2] + "BB"
	}
	if _, err := DialQUIC(context.Background(), server.addr(), DialConfig{Pin: other}); err == nil {
		t.Fatal("dial with a wrong pin succeeded")
	}
	// Without a pin the self-signed certificate does not verify against the system roots.
	if _, err := DialQUIC(context.Background(), server.addr(), DialConfig{}); err == nil {
		t.Fatal("dial without a pin accepted a self-signed certificate")
	}
}

// One UDP socket answers STUN Binding requests and carries QUIC relay sessions at the same time.
func TestSTUNAndQUICShareOneSocket(t *testing.T) {
	server := newQUICServer(t)
	acceptAll(t, server.sessions)
	responder := stunserver.New(stunserver.Options{})
	if err := responder.Serve(server.mux.STUNConn()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = responder.Close() })
	if ports := responder.Ports(); len(ports) != 1 || ports[0] != server.mux.Addr().(*net.UDPAddr).Port {
		t.Fatalf("stun ports = %v, want the mux port", ports)
	}

	link := dialFor(t, DialConfig{QUICAddrs: []string{server.addr()}, Pin: testCert.Pin, Backoff: NewQUICBackoff(time.Minute, nil)})
	if link.Kind() != KindQUIC {
		t.Fatalf("kind = %s", link.Kind())
	}

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	var response *stun.Message
	buffer := make([]byte, 1500)
	// UDP on loopback does not lose packets, but a retransmission keeps the test honest about UDP.
	for try := 0; try < 5 && response == nil; try++ {
		if _, err := client.WriteTo(request.Raw, server.mux.Addr()); err != nil {
			t.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := client.ReadFrom(buffer)
		if err != nil {
			continue
		}
		message := &stun.Message{Raw: append([]byte(nil), buffer[:n]...)}
		if err := message.Decode(); err != nil {
			t.Fatal(err)
		}
		response = message
	}
	if response == nil {
		t.Fatal("no STUN response")
	}
	var mapped stun.XORMappedAddress
	if err := mapped.GetFrom(response); err != nil {
		t.Fatal(err)
	}
	local := client.LocalAddr().(*net.UDPAddr)
	if !mapped.IP.Equal(local.IP) || mapped.Port != local.Port {
		t.Fatalf("mapped %s:%d, want %s", mapped.IP, mapped.Port, local)
	}

	// The QUIC session still works after the STUN exchange on the same port.
	if _, err := link.OpenStream(context.Background()); err != nil {
		t.Fatalf("stream after STUN: %v", err)
	}
	select {
	case <-link.Done():
		t.Fatal("STUN traffic ended the QUIC session")
	default:
	}
}
