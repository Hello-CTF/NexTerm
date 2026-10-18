//go:build unix

package agent

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

func TestDirectHelperBridgeCarriesSupervisorProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	harness := newBridgeHarness(t, ctx)
	client := supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		hello := testHello()
		hello.BridgeID = "direct-bridge"
		conn, err := DialBridge(ctx, wsURL(harness.server.server.URL), hello, false)
		if err != nil {
			return nil, err
		}
		go func() { _ = BridgePipe(ctx, conn, DirectHelperBridge, harness.stateDir) }()
		return &wsStream{conn: <-harness.accepted, ctx: ctx}, nil
	}, harness.digest)
	created, err := client.Create(ctx, supervisor.CreateOptions{
		Command: []string{"/bin/sh", "-c", "printf 'direct-ready\\n'; read line; printf 'live:%s\\n' \"$line\""},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.Attach(ctx, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, first, "direct-ready")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reattached, err := client.Attach(ctx, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reattached.Close() }()
	readUntilMarker(t, reattached, "direct-ready")
	if _, err := reattached.Write([]byte("after-reconnect\n")); err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, reattached, "live:after-reconnect")
}

type benchmarkBridgeConn struct{ io.ReadWriteCloser }

func (c benchmarkBridgeConn) LocalAddr() net.Addr              { return wsAddr{} }
func (c benchmarkBridgeConn) RemoteAddr() net.Addr             { return wsAddr{} }
func (c benchmarkBridgeConn) SetDeadline(time.Time) error      { return nil }
func (c benchmarkBridgeConn) SetReadDeadline(time.Time) error  { return nil }
func (c benchmarkBridgeConn) SetWriteDeadline(time.Time) error { return nil }

func BenchmarkHelperBridgeRoundTrip(b *testing.B) {
	stateDir := testStateDir(b)
	_, digest := testSupervisorServer(b, stateDir)
	for name, spawner := range map[string]BridgeSpawner{
		"subprocess": testBridgeSpawner(b),
		"direct":     DirectHelperBridge,
	} {
		b.Run(name, func(b *testing.B) {
			client := supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
				pipe, err := spawner(ctx, stateDir)
				if err != nil {
					return nil, err
				}
				return benchmarkBridgeConn{pipe}, nil
			}, digest)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := client.List(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
