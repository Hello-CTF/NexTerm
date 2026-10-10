package forward

import (
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestStaticForwardRelaysAndPreservesRemoteAddress(t *testing.T) {
	upstream := startEchoServer(t)
	dialer := &recordingDialer{upstream: upstream}
	provider := &switchProvider{dialer: dialer}
	service := newLoopbackService(t, provider)
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{
		SessionID:  "session-1",
		TargetHost: "remote-only.invalid",
		TargetPort: 4567,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ListenPort == 0 || spec.ListenHost != "127.0.0.1" || spec.Kind != KindLocal || spec.CreatedAt == 0 {
		t.Fatalf("spec = %+v", spec)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "static-forward")
	calls := dialer.calls()
	if len(calls) != 1 || calls[0] != "remote-only.invalid:4567" {
		t.Fatalf("SSH dial addresses = %v", calls)
	}
	if len(dialer.networks) != 1 || dialer.networks[0] != "tcp" {
		t.Fatalf("SSH dial networks = %v", dialer.networks)
	}
}

func TestStaticForwardNormalizesIPv6Literal(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "[2001:db8::1]", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "ipv6")
	if got := dialer.calls(); len(got) != 1 || got[0] != "[2001:db8::1]:22" {
		t.Fatalf("dial addresses = %v", got)
	}
}

func TestForwardUsesCurrentDialerAndRetriesReconnect(t *testing.T) {
	oldDialer := &recordingDialer{upstream: startEchoServer(t)}
	currentDialer := &recordingDialer{upstream: startEchoServer(t)}
	provider := &switchProvider{dialer: oldDialer}
	service := NewService(Config{
		Provider:    provider,
		Policy:      Policy{Desktop: true},
		Reconnect:   ReconnectPolicy{Attempts: 2, Backoff: time.Millisecond},
		DialTimeout: time.Second,
	})
	t.Cleanup(func() { _ = service.Close() })
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "internal.example", TargetPort: 22})
	if err != nil {
		t.Fatal(err)
	}
	provider.set(currentDialer)
	provider.setFailures(1)
	conn := dialForward(t, spec)
	assertEcho(t, conn, "reconnected")
	if got := len(oldDialer.calls()); got != 0 {
		t.Fatalf("stale dialer calls = %d", got)
	}
	if got := currentDialer.calls(); len(got) != 1 || got[0] != "internal.example:22" {
		t.Fatalf("current dialer calls = %v", got)
	}
	if provider.callCount() != 3 {
		t.Fatalf("provider calls = %d, want validation + failed + current", provider.callCount())
	}
}

func TestRemoveCancelsEstablishedConnectionAndListener(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "before-remove")
	readResult := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := conn.Read(one[:])
		readResult <- err
	}()
	if err := service.Remove(spec.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readResult:
		if err == nil {
			t.Fatal("established connection remained open")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Remove did not cancel the established connection")
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 100*time.Millisecond); err == nil {
		t.Fatal("listener still accepts after Remove")
	}
	if got := len(service.List()); got != 0 {
		t.Fatalf("List length = %d", got)
	}
	if err := service.Remove(spec.ID); err != nil {
		t.Fatalf("idempotent Remove: %v", err)
	}
}

func TestCloseCancelsAllForwards(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "before-close")
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("connection survived Close")
	}
	if !errors.Is(err, io.EOF) {
		var netErr net.Error
		if !errors.As(err, &netErr) {
			t.Fatalf("read error = %v", err)
		}
	}
}

func TestCloseSessionStopsOnlyThatSession(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	mine, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "session-1", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "session-2", TargetHost: "host", TargetPort: 1})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, mine)
	assertEcho(t, conn, "before-close-session")
	readResult := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := conn.Read(one[:])
		readResult <- err
	}()
	service.CloseSession("session-1")
	select {
	case err := <-readResult:
		if err == nil {
			t.Fatal("established connection survived CloseSession")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CloseSession did not cancel the established connection")
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(mine.ListenPort))), 100*time.Millisecond); err == nil {
		t.Fatal("listener still accepts after CloseSession")
	}
	remaining := service.List()
	if len(remaining) != 1 || remaining[0].ID != other.ID {
		t.Fatalf("List after CloseSession = %+v", remaining)
	}
	assertEcho(t, dialForward(t, other), "other-session-alive")
	service.CloseSession("session-1")
	service.CloseSession("session-unknown")
	if got := len(service.List()); got != 1 {
		t.Fatalf("List after idempotent CloseSession = %d", got)
	}
}

func TestExposedSOCKSRequiresExplicitRiskAcknowledgement(t *testing.T) {
	provider := &switchProvider{dialer: &recordingDialer{upstream: startEchoServer(t)}}
	service := NewService(Config{Provider: provider, Policy: Policy{}})
	service.listen = func(_, _ string) (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:0")
	}
	t.Cleanup(func() { _ = service.Close() })
	_, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s"})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("unacknowledged error = %v", err)
	}
	risk, ok := appErr.Detail.(ExposureRisk)
	if !ok || risk.Risk != "unauthenticated_exposed_socks" || risk.Authentication != "none" {
		t.Fatalf("risk detail = %#v", appErr.Detail)
	}
	if provider.callCount() != 0 {
		t.Fatal("risk rejection accessed the session")
	}
	spec, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "s", AcknowledgeRisk: true})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ListenHost != "0.0.0.0" {
		t.Fatalf("listen host = %q", spec.ListenHost)
	}
}

func TestLazyCatFailsClosedBeforeSessionLookup(t *testing.T) {
	provider := &switchProvider{err: errors.New("must not be called")}
	service := NewService(Config{Provider: provider, Policy: Policy{Platform: "LAZYCAT"}})
	t.Cleanup(func() { _ = service.Close() })
	_, err := service.CreateSocks(t.Context(), CreateSocksArgs{SessionID: "missing", AcknowledgeRisk: true})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
		t.Fatalf("LazyCat error = %v", err)
	}
	if provider.callCount() != 0 {
		t.Fatal("LazyCat creation performed session lookup")
	}
}

func TestCreateValidation(t *testing.T) {
	service := newLoopbackService(t, &switchProvider{dialer: &recordingDialer{}})
	for _, test := range []struct {
		name string
		args CreateLocalArgs
	}{
		{name: "session", args: CreateLocalArgs{TargetHost: "host", TargetPort: 1}},
		{name: "host", args: CreateLocalArgs{SessionID: "s", TargetPort: 1}},
		{name: "port", args: CreateLocalArgs{SessionID: "s", TargetHost: "host"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.CreateLocal(t.Context(), test.args)
			var appErr *ipc.Error
			if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCreateCloseRace(t *testing.T) {
	ctx := t.Context()
	for i := 0; i < 100; i++ {
		service := NewService(Config{Provider: &switchProvider{dialer: errorDialer{err: io.EOF}}, Policy: Policy{Desktop: true}})
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			_, _ = service.CreateLocal(ctx, CreateLocalArgs{SessionID: "s", TargetHost: "host", TargetPort: 1})
		}()
		close(start)
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Create/Close did not finish", i)
		}
	}
}
