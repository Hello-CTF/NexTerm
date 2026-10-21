package forward

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies the package does not leak goroutines after its tests
// finish. This package never re-executes the test binary, so every run goes
// through m.Run with no helper-process branch to exempt.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// The scenario tests below exercise the goroutine-owning forwarder paths:
// the accept loop, per-connection handlers, and the stop/reconnect cycle.
// Each closes its service (and echo server) explicitly before
// goleak.VerifyNone so the check attributes only what the scenario left
// behind. Run them in isolation for precise stacks:
//
//	go test -run TestGoleak ./internal/forward/

func goleakEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		<-done
	}
}

// 正常关闭: a local forward that relays traffic and is then shut down must
// not leave goroutines behind.
func TestGoleakForwardNormalShutdown(t *testing.T) {
	echoAddress, closeEcho := goleakEchoServer(t)
	service := newLoopbackService(t, &switchProvider{dialer: &recordingDialer{upstream: echoAddress}})
	spec, err := service.CreateLocal(context.Background(), CreateLocalArgs{SessionID: "s", TargetHost: "127.0.0.1", TargetPort: portOf(t, echoAddress)})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "normal-shutdown")
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	closeEcho()
	goleak.VerifyNone(t)
}

// 取消: removing a forward stops its accept loop and drops tracked
// connections without leaking goroutines.
func TestGoleakForwardRemove(t *testing.T) {
	echoAddress, closeEcho := goleakEchoServer(t)
	service := newLoopbackService(t, &switchProvider{dialer: &recordingDialer{upstream: echoAddress}})
	spec, err := service.CreateLocal(context.Background(), CreateLocalArgs{SessionID: "s", TargetHost: "127.0.0.1", TargetPort: portOf(t, echoAddress)})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "remove")
	if err := service.Remove(spec.ID); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	closeEcho()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

// 连接失败: an upstream that cannot be reached must fail the handler
// without leaking goroutines.
func TestGoleakForwardConnectionFailure(t *testing.T) {
	echoAddress, closeEcho := goleakEchoServer(t)
	provider := &switchProvider{dialer: &recordingDialer{upstream: echoAddress}}
	service := newLoopbackService(t, provider)
	spec, err := service.CreateLocal(context.Background(), CreateLocalArgs{SessionID: "s", TargetHost: "127.0.0.1", TargetPort: portOf(t, echoAddress)})
	if err != nil {
		t.Fatal(err)
	}
	provider.setFailures(1 << 20)
	conn := dialForward(t, spec)
	_ = conn.Close()
	provider.set(&recordingDialer{upstream: echoAddress})
	closeEcho()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

// 并发 autoStart/recovery: concurrent creates followed by a session failure
// and concurrent retries must converge without leaking goroutines.
func TestGoleakConcurrentStartAndRecovery(t *testing.T) {
	echoAddress, closeEcho := goleakEchoServer(t)
	provider := &switchProvider{dialer: &recordingDialer{upstream: echoAddress}}
	service := newLoopbackService(t, provider)
	const forwards = 8
	specs := make([]Spec, forwards)
	var wg sync.WaitGroup
	for index := 0; index < forwards; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			spec, err := service.CreateLocal(context.Background(), CreateLocalArgs{SessionID: "s", TargetHost: "127.0.0.1", TargetPort: portOf(t, echoAddress)})
			if err != nil {
				t.Error(err)
				return
			}
			specs[slot] = spec
		}(index)
	}
	wg.Wait()
	service.FailSession("s", "goleak recovery drill")
	for index := 0; index < forwards; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			if _, err := service.RetryFor(context.Background(), specs[slot].ID, "s"); err != nil {
				t.Error(err)
			}
		}(index)
	}
	wg.Wait()
	for _, spec := range specs {
		conn := dialForward(t, spec)
		assertEcho(t, conn, "recovered")
		_ = conn.Close()
	}
	closeEcho()
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

func portOf(t *testing.T, address string) uint16 {
	t.Helper()
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}
