package leg

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/p2p/natprobe/stuntest"
	"github.com/pion/ice/v4"
	"github.com/pion/transport/v5/vnet"
)

type chanSignaler struct {
	out chan<- string
	in  <-chan string
}

func (s chanSignaler) SendCandidate(ctx context.Context, c string) error {
	select {
	case s.out <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s chanSignaler) Candidates() <-chan string { return s.in }

func signalerPair() (Signaler, Signaler) {
	ab := make(chan string, 256)
	ba := make(chan string, 256)
	return chanSignaler{out: ab, in: ba}, chanSignaler{out: ba, in: ab}
}

func newKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

type side struct {
	cfg Config
	leg Leg
	err error
}

// pairConfigs returns a controlling client and a controlled agent config
// with matching pinned keys.
func pairConfigs(t testing.TB) (Config, Config) {
	ck, ak := newKey(t), newKey(t)
	return Config{Controlling: true, LocalKey: ck, PeerKey: pub(ak)},
		Config{Controlling: false, LocalKey: ak, PeerKey: pub(ck)}
}

func dialPair(t testing.TB, client, agent Config) (side, side) {
	t.Helper()
	cc, ac := NewCredentials(), NewCredentials()
	cs, as := signalerPair()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	out := [2]side{{cfg: client}, {cfg: agent}}
	wg.Add(2)
	go func() {
		defer wg.Done()
		out[0].leg, out[0].err = Dial(ctx, client, cc, ac, cs)
	}()
	go func() {
		defer wg.Done()
		out[1].leg, out[1].err = Dial(ctx, agent, ac, cc, as)
	}()
	wg.Wait()
	t.Cleanup(func() {
		for _, s := range out {
			if s.leg != nil {
				_ = s.leg.Close()
			}
		}
	})
	return out[0], out[1]
}

func mustPair(t testing.TB, client, agent Config) (Leg, Leg) {
	t.Helper()
	c, a := dialPair(t, client, agent)
	if c.err != nil || a.err != nil {
		t.Fatalf("Dial: client %v, agent %v", c.err, a.err)
	}
	return c.leg, a.leg
}

// lan builds a vnet LAN with two hosts behind one router.
func lan(t testing.TB) (*vnet.Net, *vnet.Net) {
	t.Helper()
	topo, err := stuntest.NewTopology(nil, nil, stuntest.Site{
		NAT:      &vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointIndependent},
		PublicIP: "27.1.1.1", CIDR: "192.168.0.0/24", Hosts: []string{"192.168.0.2", "192.168.0.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = topo.Close() })
	return topo.Hosts[0][0], topo.Hosts[0][1]
}

func lanConfigs(t testing.TB) (Config, Config) {
	c, a := pairConfigs(t)
	n0, n1 := lan(t)
	c.iceOptions = []ice.AgentOption{ice.WithNet(n0), ice.WithCheckInterval(20 * time.Millisecond)}
	a.iceOptions = []ice.AgentOption{ice.WithNet(n1), ice.WithCheckInterval(20 * time.Millisecond)}
	return c, a
}

func echoServer(t testing.TB, l Leg) {
	go func() {
		for {
			s, err := l.AcceptStream(context.Background())
			if err != nil {
				return
			}
			go func() {
				defer s.Close()
				_, _ = io.Copy(s, s)
			}()
		}
	}()
}

func roundTrip(t testing.TB, l Leg, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := l.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(30 * time.Second))
	errc := make(chan error, 1)
	go func() {
		_, err := s.Write(payload)
		if err == nil {
			err = s.(interface{ CloseWrite() error }).CloseWrite()
		}
		errc <- err
	}()
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("write: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatalf("echo mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestLegStreamsOverLAN(t *testing.T) {
	c, a := lanConfigs(t)
	cl, al := mustPair(t, c, a)

	echoServer(t, al)
	roundTrip(t, cl, []byte("hello"))
	roundTrip(t, cl, bytes.Repeat([]byte("x"), 1<<20))

	// The agent side can open streams too, and the peer sees a stream
	// before any application byte is written.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accepted := make(chan net.Conn, 1)
	go func() {
		s, err := cl.AcceptStream(ctx)
		if err == nil {
			accepted <- s
		}
		close(accepted)
	}()
	s, err := al.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := <-accepted
	if !ok {
		t.Fatal("client did not accept the agent's stream")
	}

	// Close on one side: the peer reads EOF and its writes fail.
	if _, err := s.Write([]byte("bye")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(peer)
	if err != nil || string(got) != "bye" {
		t.Fatalf("peer read %q, %v", got, err)
	}
	var werr error
	for i := 0; i < 50 && werr == nil; i++ {
		_, werr = peer.Write([]byte("ignored"))
		time.Sleep(10 * time.Millisecond)
	}
	if werr == nil {
		t.Fatal("writes to a closed stream kept succeeding")
	}
	if _, err := s.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after Close: %v", err)
	}
	_ = peer.Close()
}

func TestLegStatsAndPing(t *testing.T) {
	c, a := lanConfigs(t)
	cl, al := mustPair(t, c, a)
	echoServer(t, al)
	roundTrip(t, cl, bytes.Repeat([]byte("s"), 256<<10))

	rtt, err := cl.Ping(context.Background())
	if err != nil || rtt <= 0 {
		t.Fatalf("Ping = %v, %v", rtt, err)
	}
	if _, err := al.Ping(context.Background()); err != nil {
		t.Fatalf("agent Ping: %v", err)
	}
	st := cl.Stats()
	if st.RTT <= 0 || st.BytesSent < 256<<10 || st.BytesReceived < 256<<10 || st.PacketsSent == 0 {
		t.Fatalf("stats %+v", st)
	}
	if st.Since.IsZero() || time.Since(st.Since) > time.Minute {
		t.Fatalf("Since = %v", st.Since)
	}
	if st.Local.Type != "host" || st.Remote.Type != "host" || st.Local.Address != "192.168.0.2" || st.Remote.Address != "192.168.0.3" {
		t.Fatalf("selected pair %+v -> %+v", st.Local, st.Remote)
	}
	if st.Loss != 0 {
		t.Fatalf("loss on a clean link = %v", st.Loss)
	}
}

func TestLegLossAndLatency(t *testing.T) {
	c, a := lanConfigs(t)
	const oneWay = 25 * time.Millisecond
	var cget, aget func() *impairedConn
	c.PacketConnWrapper, cget = impairment(1, 0.01, oneWay)
	a.PacketConnWrapper, aget = impairment(2, 0.01, oneWay)
	cl, al := mustPair(t, c, a)
	echoServer(t, al)

	payload := make([]byte, 512<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, cl, payload)

	rtt, err := cl.Ping(context.Background())
	for i := 0; err != nil && i < 5; i++ { // a ping datagram may be among the lost 1%
		rtt, err = cl.Ping(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
	if rtt < 2*oneWay {
		t.Fatalf("ping RTT %v below the injected %v", rtt, 2*oneWay)
	}
	st := cl.Stats()
	if st.RTT < 2*oneWay || st.MinRTT < 2*oneWay {
		t.Fatalf("stats RTT %v / min %v below the injected %v", st.RTT, st.MinRTT, 2*oneWay)
	}
	// The transfer still verified intact although the wrapper dropped
	// packets in both directions, so QUIC recovered them.
	if cget().dropped.Load() == 0 || aget().dropped.Load() == 0 {
		t.Fatalf("impairment dropped nothing (client %d, agent %d)", cget().dropped.Load(), aget().dropped.Load())
	}
}

func TestLegRejectsWrongPeerKey(t *testing.T) {
	t.Run("agent presents an unpinned key", func(t *testing.T) {
		c, a := lanConfigs(t)
		c.HandshakeTimeout, a.HandshakeTimeout = 500*time.Millisecond, 500*time.Millisecond
		c.PeerKey = pub(newKey(t))
		cs, as := dialPair(t, c, a)
		if !errors.Is(cs.err, ErrPeerKey) {
			t.Fatalf("client err = %v, want ErrPeerKey", cs.err)
		}
		if as.err == nil {
			t.Fatal("agent accepted a client that rejected it")
		}
	})
	t.Run("client presents an unpinned key", func(t *testing.T) {
		c, a := lanConfigs(t)
		c.HandshakeTimeout, a.HandshakeTimeout = 500*time.Millisecond, 500*time.Millisecond
		a.PeerKey = pub(newKey(t))
		cs, as := dialPair(t, c, a)
		if !errors.Is(as.err, ErrPeerKey) {
			t.Fatalf("agent err = %v, want ErrPeerKey", as.err)
		}
		// TLS 1.3 completes on the client first; the ping round trip in
		// Dial must still fail it.
		if cs.err == nil {
			t.Fatal("client Dial succeeded although the agent rejected its key")
		}
	})
}

func TestDialRetriesLostHandshakePing(t *testing.T) {
	c, a := lanConfigs(t)
	c.HandshakeTimeout, a.HandshakeTimeout = 1500*time.Millisecond, 1500*time.Millisecond
	a.ignorePings, c.ignorePings = 2, 1
	cl, al := mustPair(t, c, a)
	if _, err := cl.Ping(context.Background()); err != nil {
		t.Fatalf("client ping after dial: %v", err)
	}
	if _, err := al.Ping(context.Background()); err != nil {
		t.Fatalf("agent ping after dial: %v", err)
	}
}

func TestLegHostOnly(t *testing.T) {
	c, a := lanConfigs(t)
	// The STUN server is unreachable; HostOnly must not wait for it.
	c.STUN = []string{"203.0.113.1:3478"}
	a.STUN = c.STUN
	c.HostOnly, a.HostOnly = true, true
	start := time.Now()
	cl, al := mustPair(t, c, a)
	if d := time.Since(start); d > DefaultHostOnlyCheckTimeout {
		t.Fatalf("HostOnly dial took %v", d)
	}
	if st := cl.Stats(); st.Local.Type != "host" || st.Remote.Type != "host" {
		t.Fatalf("pair %+v -> %+v", st.Local, st.Remote)
	}
	echoServer(t, al)
	roundTrip(t, cl, []byte("host only"))
}

func TestLegHostOnlyFailsAcrossNAT(t *testing.T) {
	topo := natTopology(t, coneNAT, coneNAT)
	c, a := natConfigs(t, topo)
	c.HostOnly, a.HostOnly = true, true
	c.CheckTimeout, a.CheckTimeout = 500*time.Millisecond, 500*time.Millisecond
	cs, as := dialPair(t, c, a)
	if !errors.Is(cs.err, ErrICE) || !errors.Is(as.err, ErrICE) {
		t.Fatalf("errs = %v / %v, want ErrICE", cs.err, as.err)
	}
}

func TestLegDoneWhenPeerCloses(t *testing.T) {
	c, a := lanConfigs(t)
	cl, al := mustPair(t, c, a)
	if err := al.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.Done():
	default:
		t.Fatal("Done not closed after Close")
	}
	select {
	case <-cl.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not notice the peer closing")
	}
	if _, err := cl.OpenStream(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("OpenStream after death: %v", err)
	}
	if _, err := cl.Ping(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Ping after death: %v", err)
	}
}

func TestLegDoneWhenPathGoesSilent(t *testing.T) {
	c, a := lanConfigs(t)
	cw, cget := impairment(3, 0, 0)
	aw, aget := impairment(4, 0, 0)
	c.PacketConnWrapper, a.PacketConnWrapper = cw, aw
	c.idleTimeout, a.idleTimeout = 600*time.Millisecond, 600*time.Millisecond
	cl, al := mustPair(t, c, a)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := cl.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cget().setBlackout(true)
	aget().setBlackout(true)
	for _, l := range []Leg{cl, al} {
		select {
		case <-l.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("leg not Done after the path went silent")
		}
	}
	if _, err := s.Write([]byte("x")); err == nil {
		_, err = s.Read(make([]byte, 1))
		if err == nil {
			t.Fatal("stream still usable after the leg died")
		}
	}
}

func TestLegManyConcurrentStreams(t *testing.T) {
	c, a := lanConfigs(t)
	cl, al := mustPair(t, c, a)
	echoServer(t, al)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, err := cl.OpenStream(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			_ = s.SetDeadline(time.Now().Add(10 * time.Second))
			msg := bytes.Repeat([]byte{byte(i)}, 4096+i)
			if _, err := s.Write(msg); err != nil {
				errs <- err
				return
			}
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(s, got); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, msg) {
				errs <- errors.New("stream data crossed")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestLegCloseDuringStreams(t *testing.T) {
	c, a := lanConfigs(t)
	cl, al := mustPair(t, c, a)
	echoServer(t, al)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := cl.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, s)
		readErr <- err
	}()
	acceptErr := make(chan error, 1)
	go func() {
		_, err := cl.AcceptStream(context.Background())
		acceptErr <- err
	}()
	if err := cl.Close(); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]chan error{"read": readErr, "accept": acceptErr} {
		select {
		case err := <-ch:
			if err == nil {
				t.Fatalf("%s returned nil after Close", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s blocked after Close", name)
		}
	}
	if err := cl.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestDialCancelledBeforePeerAnswers(t *testing.T) {
	c, _ := lanConfigs(t)
	cs, _ := signalerPair()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Dial(ctx, c, NewCredentials(), NewCredentials(), cs)
	if !errors.Is(err, ErrICE) {
		t.Fatalf("err = %v, want ErrICE", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Dial ignored its context for %v", d)
	}
}

func TestDialValidatesConfig(t *testing.T) {
	c, _ := pairConfigs(t)
	cs, _ := signalerPair()
	good := NewCredentials()
	cases := map[string]func() (Config, Credentials, Signaler){
		"short local key": func() (Config, Credentials, Signaler) { x := c; x.LocalKey = x.LocalKey[:10]; return x, good, cs },
		"no peer key":     func() (Config, Credentials, Signaler) { x := c; x.PeerKey = nil; return x, good, cs },
		"nil signaler":    func() (Config, Credentials, Signaler) { return c, good, nil },
		"short pwd":       func() (Config, Credentials, Signaler) { return c, Credentials{Ufrag: "abcd", Pwd: "short"}, cs },
		"bad stun":        func() (Config, Credentials, Signaler) { x := c; x.STUN = []string{"no-port"}; return x, good, cs },
	}
	for name, mk := range cases {
		cfg, cred, sig := mk()
		if _, err := Dial(context.Background(), cfg, cred, good, sig); err == nil {
			t.Errorf("%s: Dial succeeded", name)
		}
	}
}

func TestNewCredentials(t *testing.T) {
	a, b := NewCredentials(), NewCredentials()
	if a == b {
		t.Fatal("credentials repeat")
	}
	if len(a.Ufrag) < 4 || len(a.Pwd) < 22 {
		t.Fatalf("credentials too short: %+v", a)
	}
	const iceChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for _, r := range a.Ufrag + a.Pwd {
		if !bytes.ContainsRune([]byte(iceChars), r) {
			t.Fatalf("non ice-char %q", r)
		}
	}
}

func TestLegRealUDPLoopback(t *testing.T) {
	c, a := pairConfigs(t)
	loopbackOnly := []ice.AgentOption{
		ice.WithIncludeLoopback(),
		ice.WithIPFilter(func(ip net.IP) bool { return ip.IsLoopback() && ip.To4() != nil }),
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4}),
		ice.WithCheckInterval(20 * time.Millisecond),
	}
	c.iceOptions, a.iceOptions = loopbackOnly, loopbackOnly
	cl, al := mustPair(t, c, a)
	if st := cl.Stats(); st.Local.Address != "127.0.0.1" || st.Remote.Address != "127.0.0.1" || st.Local.Network != "udp4" {
		t.Fatalf("pair %+v -> %+v", st.Local, st.Remote)
	}
	echoServer(t, al)
	roundTrip(t, cl, bytes.Repeat([]byte("udp"), 300_000))
	if _, err := cl.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Closing a stream unblocks a Write stuck on a peer that stopped reading, so a carrier the rstream layer
// abandons never holds its writer until the QUIC idle timeout.
func TestStreamCloseUnblocksAStuckWrite(t *testing.T) {
	c, a := pairConfigs(t)
	c.LoopbackOnly, a.LoopbackOnly = true, true
	cl, al := mustPair(t, c, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accepted := make(chan net.Conn, 1)
	go func() {
		s, err := al.AcceptStream(ctx)
		if err == nil {
			accepted <- s
		}
	}()
	s, err := cl.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	defer peer.Close()
	written := make(chan error, 1)
	go func() {
		// Far beyond the flow-control windows: the peer never reads, so this blocks.
		_, err := s.Write(make([]byte, 64<<20))
		written <- err
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-written:
		t.Fatalf("write returned before close: %v", err)
	default:
	}
	_ = s.Close()
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("aborted write reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not unblock the write")
	}
}
