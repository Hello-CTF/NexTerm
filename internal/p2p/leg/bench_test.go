package leg

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/quic-go/quic-go"
)

// Impairment for the flip-condition benchmark (design 1.2): 25 ms each way
// (50 ms RTT) and 1% independent loss in each direction.
const (
	benchOneWay = 25 * time.Millisecond
	benchLoss   = 0.01
)

func loopbackOptions() []ice.AgentOption {
	return []ice.AgentOption{
		ice.WithIncludeLoopback(),
		ice.WithIPFilter(func(ip net.IP) bool { return ip.IsLoopback() && ip.To4() != nil }),
		ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4}),
		ice.WithCheckInterval(20 * time.Millisecond),
	}
}

// BenchmarkLegThroughput sends one-directional bulk data over a leg on real
// 127.0.0.1 UDP sockets.
func BenchmarkLegThroughput(b *testing.B) {
	for _, bc := range []struct {
		name  string
		loss  float64
		delay time.Duration
		chunk int
	}{
		{"clean", 0, 0, 16 << 20},
		{"rtt50ms_loss1pct", benchLoss, benchOneWay, 2 << 20},
	} {
		b.Run(bc.name, func(b *testing.B) {
			c, a := pairConfigs(b)
			c.iceOptions, a.iceOptions = loopbackOptions(), loopbackOptions()
			if bc.loss > 0 || bc.delay > 0 {
				c.PacketConnWrapper, _ = impairment(11, bc.loss, bc.delay)
				a.PacketConnWrapper, _ = impairment(12, bc.loss, bc.delay)
			}
			cl, al := mustPair(b, c, a)
			go sink(func(ctx context.Context) (net.Conn, error) { return al.AcceptStream(ctx) })
			bulk(b, func(ctx context.Context) (net.Conn, error) { return cl.OpenStream(ctx) }, bc.chunk)
		})
	}
}

// BenchmarkQUICDirectUDP is the same transfer with quic-go on a plain UDP
// socket (no ICE adapter), hidden behind the same wrapper so neither side
// gets GSO or ECN. The gap to BenchmarkLegThroughput is the adapter's cost.
func BenchmarkQUICDirectUDP(b *testing.B) {
	for _, bc := range []struct {
		name  string
		loss  float64
		delay time.Duration
		chunk int
	}{
		{"clean", 0, 0, 16 << 20},
		{"rtt50ms_loss1pct", benchLoss, benchOneWay, 2 << 20},
	} {
		b.Run(bc.name, func(b *testing.B) {
			srvUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			cliUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			srvPC := newImpaired(srvUDP, 21, bc.loss, bc.delay)
			cliPC := newImpaired(cliUDP, 22, bc.loss, bc.delay)
			srvTr := &quic.Transport{Conn: srvPC}
			cliTr := &quic.Transport{Conn: cliPC}
			b.Cleanup(func() {
				_ = cliTr.Close()
				_ = srvTr.Close()
				_ = cliPC.Close()
				_ = srvPC.Close()
			})
			ck, ak := newKey(b), newKey(b)
			stls, _ := serverTLSConfig(ak, &peerVerifier{key: pub(ck)})
			ctls, _ := clientTLSConfig(ck, &peerVerifier{key: pub(ak)})
			qconf := quicConfig(5*time.Second, QUICIdleTimeout)
			ln, err := srvTr.Listen(stls, qconf)
			if err != nil {
				b.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			srvConn := make(chan *quic.Conn, 1)
			go func() {
				c, err := ln.Accept(ctx)
				if err == nil {
					srvConn <- c
				}
				close(srvConn)
			}()
			cc, err := cliTr.Dial(ctx, srvUDP.LocalAddr(), ctls, qconf)
			if err != nil {
				b.Fatal(err)
			}
			sc, ok := <-srvConn
			if !ok {
				b.Fatal("server accept failed")
			}
			b.Cleanup(func() {
				_ = cc.CloseWithError(0, "")
				_ = sc.CloseWithError(0, "")
			})
			go sink(func(ctx context.Context) (net.Conn, error) {
				s, err := sc.AcceptStream(ctx)
				if err != nil {
					return nil, err
				}
				return newStream(s, sc), nil
			})
			bulk(b, func(ctx context.Context) (net.Conn, error) {
				s, err := cc.OpenStreamSync(ctx)
				if err != nil {
					return nil, err
				}
				return newStream(s, cc), nil
			}, bc.chunk)
		})
	}
}

// BenchmarkTCPLoopback is a clean-link reference for the relay's transport.
// Loopback TCP cannot be impaired from user space, so there is no lossy
// variant; see the report for the relay comparison limits.
func BenchmarkTCPLoopback(b *testing.B) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = ln.Close() })
	go sink(func(context.Context) (net.Conn, error) { return ln.Accept() })
	bulk(b, func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp4", ln.Addr().String())
	}, 16<<20)
}

func sink(accept func(context.Context) (net.Conn, error)) {
	for {
		s, err := accept(context.Background())
		if err != nil {
			return
		}
		go func() {
			defer s.Close()
			if _, err := io.Copy(io.Discard, s); err == nil {
				_, _ = s.Write([]byte{1})
			}
		}()
	}
}

func bulk(b *testing.B, open func(context.Context) (net.Conn, error), chunk int) {
	buf := make([]byte, 64<<10)
	b.SetBytes(int64(chunk))
	b.ResetTimer()
	for range b.N {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		s, err := open(ctx)
		cancel()
		if err != nil {
			b.Fatal(err)
		}
		for sent := 0; sent < chunk; sent += len(buf) {
			if _, err := s.Write(buf); err != nil {
				b.Fatal(err)
			}
		}
		if cw, ok := s.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil {
				b.Fatal(err)
			}
		}
		ack := make([]byte, 1)
		if _, err := io.ReadFull(s, ack); err != nil {
			b.Fatal(err)
		}
		_ = s.Close()
	}
}
