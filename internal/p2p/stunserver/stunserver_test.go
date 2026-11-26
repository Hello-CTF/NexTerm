package stunserver

import (
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v4"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func bindingRequest(t *testing.T) *stun.Message {
	t.Helper()
	message, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func decodeMapped(t *testing.T, raw []byte, request *stun.Message) stun.XORMappedAddress {
	t.Helper()
	response := &stun.Message{Raw: append([]byte(nil), raw...)}
	if err := response.Decode(); err != nil {
		t.Fatal(err)
	}
	if response.Type != stun.BindingSuccess {
		t.Fatalf("type = %v", response.Type)
	}
	if response.TransactionID != request.TransactionID {
		t.Fatal("transaction id not echoed")
	}
	if err := stun.Fingerprint.Check(response); err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	var mapped stun.XORMappedAddress
	if err := mapped.GetFrom(response); err != nil {
		t.Fatal(err)
	}
	return mapped
}

func TestServerAnswersBindingWithTheSourceAddressOnEveryListener(t *testing.T) {
	server := New(Options{})
	t.Cleanup(func() { _ = server.Close() })
	var addresses []net.Addr
	for range 2 {
		address, err := server.Listen("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addresses = append(addresses, address)
	}
	if ports := server.Ports(); len(ports) != 2 || ports[0] == ports[1] {
		t.Fatalf("ports = %v", ports)
	}
	for _, address := range addresses {
		client, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		request := bindingRequest(t)
		if _, err := client.WriteTo(request.Raw, address); err != nil {
			t.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		buffer := make([]byte, 1500)
		n, _, err := client.ReadFrom(buffer)
		if err != nil {
			t.Fatal(err)
		}
		mapped := decodeMapped(t, buffer[:n], request)
		local := client.LocalAddr().(*net.UDPAddr)
		if !mapped.IP.Equal(local.IP) || mapped.Port != local.Port {
			t.Fatalf("mapped = %s, want %s", mapped.String(), local)
		}
		_ = client.Close()
	}
}

func TestRespondMapsIPv6Sources(t *testing.T) {
	server := New(Options{})
	request := bindingRequest(t)
	source := &net.UDPAddr{IP: net.ParseIP("2001:db8::7"), Port: 40000}
	raw, ok := server.respond(request.Raw, source)
	if !ok {
		t.Fatal("dropped")
	}
	mapped := decodeMapped(t, raw, request)
	if !mapped.IP.Equal(source.IP) || mapped.Port != source.Port {
		t.Fatalf("mapped = %s", mapped.String())
	}
	if len(raw) > 52 {
		t.Fatalf("response is %d bytes", len(raw))
	}
}

func TestRespondDropsAnythingButABindingRequest(t *testing.T) {
	server := New(Options{})
	source := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 5000}
	success, err := stun.Build(stun.TransactionID, stun.BindingSuccess)
	if err != nil {
		t.Fatal(err)
	}
	allocate, err := stun.Build(stun.TransactionID, stun.NewType(stun.MethodAllocate, stun.ClassRequest))
	if err != nil {
		t.Fatal(err)
	}
	for name, packet := range map[string][]byte{
		"garbage":          []byte("GET / HTTP/1.1\r\n\r\n"),
		"empty":            nil,
		"binding success":  success.Raw,
		"allocate request": allocate.Raw,
		"truncated":        bindingRequest(t).Raw[:12],
	} {
		if _, ok := server.respond(packet, source); ok {
			t.Fatalf("%s answered", name)
		}
	}
	if _, ok := server.respond(bindingRequest(t).Raw, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 0}); ok {
		t.Fatal("port 0 source answered")
	}
}

func TestRespondRateLimitsEachSourceIP(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	server := New(Options{Rate: 1, Burst: 3, Now: clock.Now})
	first := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 1), Port: 1000}
	samePeerOtherPort := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 1), Port: 2000}
	other := &net.UDPAddr{IP: net.IPv4(198, 51, 100, 2), Port: 1000}
	answered := func(source *net.UDPAddr) bool {
		_, ok := server.respond(bindingRequest(t).Raw, source)
		return ok
	}
	for i := range 3 {
		if !answered(first) {
			t.Fatalf("request %d within burst dropped", i)
		}
	}
	if answered(samePeerOtherPort) {
		t.Fatal("a new source port of the same IP escaped the limit")
	}
	if !answered(other) {
		t.Fatal("another IP was limited")
	}
	clock.Advance(time.Second)
	if !answered(first) {
		t.Fatal("token did not refill")
	}
	if answered(first) {
		t.Fatal("refill exceeded the rate")
	}
}

func TestLimiterCapsTrackedSourcesAndEvictsRefilledOnes(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	limiter := NewLimiter(1, 2, 2, clock.Now)
	a, b, c := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")
	if !limiter.Allow(a) || !limiter.Allow(b) {
		t.Fatal("first sources dropped")
	}
	if limiter.Allow(c) {
		t.Fatal("a source beyond the cap was tracked while others are still limited")
	}
	clock.Advance(2 * time.Second)
	if !limiter.Allow(c) {
		t.Fatal("refilled buckets were not evicted")
	}
}

func TestCloseStopsServing(t *testing.T) {
	server := New(Options{})
	if _, err := server.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Listen("127.0.0.1:0"); err == nil {
		t.Fatal("listen after close succeeded")
	}
}
