package forward

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type fakeRemoteDialer struct {
	*recordingDialer
	mu        sync.Mutex
	listeners []net.Listener
	addresses []string
}

type blockedRebindDialer struct {
	*fakeRemoteDialer
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (d *blockedRebindDialer) ListenRemote(ctx context.Context, network, address string) (net.Listener, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call == 2 {
		close(d.entered)
		<-d.release
	}
	return d.fakeRemoteDialer.ListenRemote(ctx, network, address)
}

func (d *fakeRemoteDialer) ListenRemote(_ context.Context, network, address string) (net.Listener, error) {
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.listeners = append(d.listeners, listener)
	d.addresses = append(d.addresses, address)
	d.mu.Unlock()
	return listener, nil
}

func (d *fakeRemoteDialer) listenCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addresses...)
}

func (d *fakeRemoteDialer) closeListeners() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, listener := range d.listeners {
		_ = listener.Close()
	}
}

func echoTargetPort(t *testing.T) uint16 {
	t.Helper()
	_, portText, err := net.SplitHostPort(startEchoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}

func TestRemoteForwardRelaysToLocalTarget(t *testing.T) {
	targetPort := echoTargetPort(t)
	dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		BindHost:   "127.0.0.1",
		BindPort:   0,
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != KindRemote || spec.ListenHost != "127.0.0.1" || spec.ListenPort == 0 || spec.CreatedAt == 0 {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.TargetHost == nil || *spec.TargetHost != "127.0.0.1" || spec.TargetPort == nil || *spec.TargetPort != targetPort {
		t.Fatalf("spec target = %v/%v", spec.TargetHost, spec.TargetPort)
	}
	if calls := dialer.listenCalls(); len(calls) != 1 || calls[0] != "127.0.0.1:0" {
		t.Fatalf("remote listen calls = %v", calls)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "remote-forward")
}

func TestRemoteForwardDefaultsToLoopbackBind(t *testing.T) {
	dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		TargetHost: "127.0.0.1",
		TargetPort: echoTargetPort(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ListenHost != "127.0.0.1" {
		t.Fatalf("listen host = %q", spec.ListenHost)
	}
	if calls := dialer.listenCalls(); len(calls) != 1 || calls[0] != "127.0.0.1:0" {
		t.Fatalf("remote listen calls = %v", calls)
	}
}

func TestRemoteForwardExposedBindRequiresAcknowledgement(t *testing.T) {
	provider := &switchProvider{dialer: &fakeRemoteDialer{recordingDialer: &recordingDialer{}}}
	service := newLoopbackService(t, provider)
	_, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		BindHost:   "0.0.0.0",
		TargetHost: "127.0.0.1",
		TargetPort: 8080,
	})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNeedsConfirm {
		t.Fatalf("unacknowledged error = %v", err)
	}
	risk, ok := appErr.Detail.(ExposureRisk)
	if !ok || risk.Risk != "unauthenticated_exposed_remote_forward" || risk.ListenHost != "0.0.0.0" || risk.Authentication != "none" {
		t.Fatalf("risk detail = %#v", appErr.Detail)
	}
	if provider.callCount() != 0 {
		t.Fatal("risk rejection accessed the session")
	}
	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:       "s",
		BindHost:        "0.0.0.0",
		TargetHost:      "127.0.0.1",
		TargetPort:      8080,
		AcknowledgeRisk: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ListenHost != "0.0.0.0" {
		t.Fatalf("listen host = %q", spec.ListenHost)
	}
}

func TestRemoteForwardLoopbackBindTableNeedsNoAcknowledgement(t *testing.T) {
	for _, bindHost := range []string{"127.0.0.1", "localhost", "::1", "127.0.0.2"} {
		t.Run(bindHost, func(t *testing.T) {
			dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
			service := newLoopbackService(t, &switchProvider{dialer: dialer})
			spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
				SessionID:  "s",
				BindHost:   bindHost,
				TargetHost: "127.0.0.1",
				TargetPort: 8080,
			})
			if err != nil {
				// Some hosts cannot bind non-primary loopback addresses; the
				// classification still must not demand a risk acknowledgement.
				var appErr *ipc.Error
				if errors.As(err, &appErr) && appErr.Code == ipc.CodeNeedsConfirm {
					t.Fatalf("loopback bind %q demanded acknowledgement: %v", bindHost, err)
				}
				return
			}
			if spec.ListenHost != bindHost {
				t.Fatalf("listen host = %q, want %q", spec.ListenHost, bindHost)
			}
			if calls := dialer.listenCalls(); len(calls) != 1 {
				t.Fatalf("remote listen calls = %v", calls)
			}
		})
	}
}

func TestRemoteForwardExposedBindTableDemandsAcknowledgement(t *testing.T) {
	for _, bindHost := range []string{"0.0.0.0", "::", "192.0.2.1", "example.com"} {
		t.Run(bindHost, func(t *testing.T) {
			dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
			provider := &switchProvider{dialer: dialer}
			service := newLoopbackService(t, provider)
			_, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
				SessionID:  "s",
				BindHost:   bindHost,
				TargetHost: "127.0.0.1",
				TargetPort: 8080,
			})
			var appErr *ipc.Error
			if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNeedsConfirm {
				t.Fatalf("bind %q error = %v, want needs_confirm", bindHost, err)
			}
			risk, ok := appErr.Detail.(ExposureRisk)
			if !ok || risk.Risk != "unauthenticated_exposed_remote_forward" || risk.ListenHost != bindHost || risk.Authentication != "none" {
				t.Fatalf("risk detail = %#v", appErr.Detail)
			}
			if provider.callCount() != 0 {
				t.Fatal("risk rejection accessed the session")
			}
			if calls := dialer.listenCalls(); len(calls) != 0 {
				t.Fatalf("risk rejection opened a remote listener: %v", calls)
			}
			if runtimes := service.List(); len(runtimes) != 0 {
				t.Fatalf("risk rejection left a runtime: %+v", runtimes)
			}
		})
	}
}

func TestRemoteForwardUnsupportedTransport(t *testing.T) {
	service := newLoopbackService(t, &switchProvider{dialer: &recordingDialer{}})
	_, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		TargetHost: "127.0.0.1",
		TargetPort: 8080,
	})
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
		t.Fatalf("unsupported transport error = %v", err)
	}
}

func TestRemoteForwardValidation(t *testing.T) {
	service := newLoopbackService(t, &switchProvider{dialer: &fakeRemoteDialer{recordingDialer: &recordingDialer{}}})
	for _, test := range []struct {
		name string
		args CreateRemoteArgs
	}{
		{name: "session", args: CreateRemoteArgs{TargetHost: "127.0.0.1", TargetPort: 1}},
		{name: "host", args: CreateRemoteArgs{SessionID: "s", TargetPort: 1}},
		{name: "port", args: CreateRemoteArgs{SessionID: "s", TargetHost: "127.0.0.1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.CreateRemote(t.Context(), test.args)
			var appErr *ipc.Error
			if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRemoteForwardRemoveReleasesRemotePort(t *testing.T) {
	dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		TargetHost: "127.0.0.1",
		TargetPort: echoTargetPort(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "before-remove")
	if err := service.Remove(spec.ID); err != nil {
		t.Fatal(err)
	}
	if conn2, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 100*time.Millisecond); err == nil {
		conn2.Close()
		t.Fatal("remote port still accepts after Remove")
	}
	if got := len(service.List()); got != 0 {
		t.Fatalf("List length = %d", got)
	}
}

func TestRemoteForwardListenerFailureRemainsRetryable(t *testing.T) {
	dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
	reported := make(chan error, 4)
	service := NewService(Config{
		Provider:    &switchProvider{dialer: dialer},
		Policy:      Policy{Desktop: true},
		DialTimeout: time.Second,
		OnError:     func(err error) { reported <- err },
	})
	t.Cleanup(func() { _ = service.Close() })
	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		TargetHost: "127.0.0.1",
		TargetPort: echoTargetPort(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	dialer.closeListeners()
	select {
	case err := <-reported:
		if err == nil {
			t.Fatal("reported error is nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote listener failure was not reported")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		listed := service.List()
		if len(listed) == 1 && listed[0].ID == spec.ID && listed[0].Status == StatusFailed && listed[0].Error != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed remote listener not visible: %+v", listed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	retried, err := service.RetryFor(t.Context(), spec.ID, "")
	if err != nil || retried.ID != spec.ID || retried.ListenPort != spec.ListenPort || retried.Status != StatusActive {
		t.Fatalf("retry = %+v, %v", retried, err)
	}
	assertEcho(t, dialForward(t, retried), "after-retry")
}

func TestRemoteRetryDoesNotResurrectDisconnectedSession(t *testing.T) {
	dialer := &blockedRebindDialer{
		fakeRemoteDialer: &fakeRemoteDialer{recordingDialer: &recordingDialer{}},
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	spec, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID: "s", TargetHost: "127.0.0.1", TargetPort: echoTargetPort(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	service.SuspendRemote("s")
	done := make(chan error, 1)
	go func() {
		_, err := service.RetryFor(t.Context(), spec.ID, "")
		done <- err
	}()
	select {
	case <-dialer.entered:
	case <-time.After(2 * time.Second):
		close(dialer.release)
		t.Fatal("retry did not reach remote listener rebind")
	}
	service.FailSession("s", "session disconnected")
	close(dialer.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("retry succeeded after session disconnected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not finish")
	}
	listed := service.List()
	if len(listed) != 1 || listed[0].ID != spec.ID || listed[0].Status != StatusFailed || listed[0].Error != "session disconnected" {
		t.Fatalf("disconnected forward state = %+v", listed)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("remote listener remained open after disconnection")
	}
}

func TestRemoteCreateCloseRace(t *testing.T) {
	ctx := t.Context()
	for i := 0; i < 100; i++ {
		service := NewService(Config{
			Provider: &switchProvider{dialer: &fakeRemoteDialer{recordingDialer: &recordingDialer{}}},
			Policy:   Policy{Desktop: true},
		})
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-start
			_, _ = service.CreateRemote(ctx, CreateRemoteArgs{SessionID: "s", TargetHost: "127.0.0.1", TargetPort: 1})
		}()
		close(start)
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: CreateRemote/Close did not finish", i)
		}
	}
}
