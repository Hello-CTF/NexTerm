package forward

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	gossh "golang.org/x/crypto/ssh"
)

func TestSOCKSConnectAddressTypesAndRemoteDNS(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		addressType byte
		host        string
		want        string
	}{
		{name: "IPv4", addressType: 1, host: "192.0.2.10", want: "192.0.2.10:8741"},
		{name: "IPv6", addressType: 4, host: "2001:db8::10", want: "[2001:db8::10]:8741"},
		{name: "domain is not resolved locally", addressType: 3, host: "remote-dns-only.invalid", want: "remote-dns-only.invalid:8741"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := dialForward(t, spec)
			negotiateTestSOCKS(t, conn)
			writeTestSOCKSRequest(t, conn, 1, test.addressType, test.host, 8741)
			if reply := readTestSOCKSReply(t, conn); reply != 0 {
				t.Fatalf("reply = %d", reply)
			}
			assertEcho(t, conn, test.name)
		})
	}
	calls := dialer.calls()
	if len(calls) != len(tests) {
		t.Fatalf("dial calls = %v", calls)
	}
	for i, test := range tests {
		if calls[i] != test.want {
			t.Fatalf("dial %d = %q, want %q", i, calls[i], test.want)
		}
		if dialer.networks[i] != "tcp" {
			t.Fatalf("network %d = %q", i, dialer.networks[i])
		}
	}
}

func TestSOCKSSupportsOnlyNoAuthCONNECT(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("authentication required client", func(t *testing.T) {
		conn := dialForward(t, spec)
		if err := writeAll(conn, []byte{5, 1, 2}); err != nil {
			t.Fatal(err)
		}
		var response [2]byte
		if _, err := io.ReadFull(conn, response[:]); err != nil {
			t.Fatal(err)
		}
		if response != [2]byte{5, 0xff} {
			t.Fatalf("response = %v", response)
		}
	})

	for _, command := range []byte{2, 3} {
		t.Run("command "+string(rune('0'+command)), func(t *testing.T) {
			conn := dialForward(t, spec)
			negotiateTestSOCKS(t, conn)
			writeTestSOCKSRequest(t, conn, command, 3, "example.com", 80)
			if reply := readTestSOCKSReply(t, conn); reply != 7 {
				t.Fatalf("reply = %d", reply)
			}
		})
	}
	if got := dialer.calls(); len(got) != 0 {
		t.Fatalf("unsupported commands opened upstream: %v", got)
	}
}

func TestSOCKSRejectsMalformedAddressAndReservedByte(t *testing.T) {
	service := newLoopbackService(t, &switchProvider{dialer: &recordingDialer{upstream: startEchoServer(t)}})
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		request []byte
	}{
		{name: "address type", request: []byte{5, 1, 0, 9}},
		{name: "reserved", request: []byte{5, 1, 1, 1}},
		{name: "empty domain", request: []byte{5, 1, 0, 3, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := dialForward(t, spec)
			negotiateTestSOCKS(t, conn)
			if err := writeAll(conn, test.request); err != nil {
				t.Fatal(err)
			}
			if reply := readTestSOCKSReply(t, conn); reply != 8 {
				t.Fatalf("reply = %d", reply)
			}
		})
	}
}

func TestSOCKSTransportFailureReturnsGeneralFailure(t *testing.T) {
	provider := &switchProvider{err: io.EOF}
	service := newLoopbackService(t, provider)
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err == nil {
		_ = service.Remove(spec.ID)
		t.Fatal("creation succeeded with unavailable provider")
	}
	provider.err = nil
	provider.dialer = errorDialer{err: io.EOF}
	spec, err = service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	negotiateTestSOCKS(t, conn)
	writeTestSOCKSRequest(t, conn, 1, 3, "example.com", 80)
	if reply := readTestSOCKSReply(t, conn); reply != socksGeneralFailure {
		t.Fatalf("reply = %d", reply)
	}
}

type countingDialer struct {
	calls atomic.Int32
	dial  func(context.Context) (net.Conn, error)
}

func (d *countingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	d.calls.Add(1)
	return d.dial(ctx)
}

func newSOCKSRetryService(t *testing.T, dialer base.Dialer, handshake time.Duration) Spec {
	t.Helper()
	service := NewService(Config{
		Provider:         &switchProvider{dialer: dialer},
		Policy:           Policy{Desktop: true},
		Reconnect:        ReconnectPolicy{Attempts: 4, Backoff: time.Millisecond},
		DialTimeout:      time.Minute,
		HandshakeTimeout: handshake,
	})
	t.Cleanup(func() { _ = service.Close() })
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestSOCKSOpenChannelRejectionIsNotRetriedAndMapsReply(t *testing.T) {
	tests := []struct {
		name    string
		reason  gossh.RejectionReason
		message string
		want    byte
	}{
		{name: "refused", reason: gossh.ConnectionFailed, message: "Connection refused", want: socksConnectionRefused},
		{name: "no route", reason: gossh.ConnectionFailed, message: "No route to host", want: socksHostUnreachable},
		{name: "dns", reason: gossh.ConnectionFailed, message: "Name or service not known", want: socksHostUnreachable},
		{name: "network", reason: gossh.ConnectionFailed, message: "Network is unreachable", want: socksNetworkUnreachable},
		{name: "host down", reason: gossh.ConnectionFailed, message: "Host is down", want: socksHostUnreachable},
		{name: "prohibited", reason: gossh.Prohibited, message: "administratively prohibited", want: socksNotAllowed},
		{name: "other", reason: gossh.ResourceShortage, message: "too many channels", want: socksGeneralFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			openErr := &gossh.OpenChannelError{Reason: test.reason, Message: test.message}
			dialer := &countingDialer{dial: func(context.Context) (net.Conn, error) {
				return nil, fmt.Errorf("SSH tcp dial target: %w", openErr)
			}}
			spec := newSOCKSRetryService(t, dialer, 5*time.Second)
			conn := dialForward(t, spec)
			negotiateTestSOCKS(t, conn)
			writeTestSOCKSRequest(t, conn, 1, 3, "example.com", 80)
			if reply := readTestSOCKSReply(t, conn); reply != test.want {
				t.Fatalf("reply = %d, want %d", reply, test.want)
			}
			if calls := dialer.calls.Load(); calls != 1 {
				t.Fatalf("dial calls = %d, want 1", calls)
			}
		})
	}
}

func TestSOCKSBlackholedTargetRepliesBeforeHandshakeDeadline(t *testing.T) {
	const handshake = 2 * time.Second
	dialer := &countingDialer{dial: func(ctx context.Context) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	spec := newSOCKSRetryService(t, dialer, handshake)
	conn := dialForward(t, spec)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	negotiateTestSOCKS(t, conn)
	writeTestSOCKSRequest(t, conn, 1, 3, "blackhole.example", 80)
	if reply := readTestSOCKSReply(t, conn); reply != socksHostUnreachable {
		t.Fatalf("reply = %d, want %d", reply, socksHostUnreachable)
	}
	if elapsed := time.Since(started); elapsed >= handshake {
		t.Fatalf("reply after %v, want before handshake deadline %v", elapsed, handshake)
	}
	if calls := dialer.calls.Load(); calls != 1 {
		t.Fatalf("dial calls = %d, want 1", calls)
	}
}

func TestSOCKSTransportFailureIsRetried(t *testing.T) {
	upstream := startEchoServer(t)
	dialer := &countingDialer{}
	dialer.dial = func(ctx context.Context) (net.Conn, error) {
		if dialer.calls.Load() == 1 {
			return nil, io.EOF
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream)
	}
	spec := newSOCKSRetryService(t, dialer, 5*time.Second)
	conn := dialForward(t, spec)
	negotiateTestSOCKS(t, conn)
	writeTestSOCKSRequest(t, conn, 1, 3, "example.com", 80)
	if reply := readTestSOCKSReply(t, conn); reply != socksSucceeded {
		t.Fatalf("reply = %d", reply)
	}
	assertEcho(t, conn, "after-retry")
	if calls := dialer.calls.Load(); calls != 2 {
		t.Fatalf("dial calls = %d, want 2", calls)
	}
}

func TestLocalForwardRetriesAfterAttemptTimeout(t *testing.T) {
	upstream := startEchoServer(t)
	dialer := &countingDialer{}
	dialer.dial = func(ctx context.Context) (net.Conn, error) {
		if dialer.calls.Load() == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream)
	}
	service := NewService(Config{
		Provider:    &switchProvider{dialer: dialer},
		Policy:      Policy{Desktop: true},
		Reconnect:   ReconnectPolicy{Attempts: 2, Backoff: time.Millisecond},
		DialTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = service.Close() })
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "internal.example", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "after-attempt-timeout")
	if calls := dialer.calls.Load(); calls != 2 {
		t.Fatalf("dial calls = %d, want 2", calls)
	}
}

func TestSOCKSHandshakeHasDeadline(t *testing.T) {
	service := NewService(Config{
		Provider:         &switchProvider{dialer: &recordingDialer{}},
		Policy:           Policy{Desktop: true},
		HandshakeTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = service.Close() })
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("idle handshake remained open")
	}
}
