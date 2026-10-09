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

func TestRemoteForwardListenerFailureEvictsAndReports(t *testing.T) {
	dialer := &fakeRemoteDialer{recordingDialer: &recordingDialer{}}
	reported := make(chan error, 4)
	service := NewService(Config{
		Provider:    &switchProvider{dialer: dialer},
		Policy:      Policy{Desktop: true},
		DialTimeout: time.Second,
		OnError:     func(err error) { reported <- err },
	})
	t.Cleanup(func() { _ = service.Close() })
	if _, err := service.CreateRemote(t.Context(), CreateRemoteArgs{
		SessionID:  "s",
		TargetHost: "127.0.0.1",
		TargetPort: echoTargetPort(t),
	}); err != nil {
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
	for len(service.List()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("failed remote listener was not evicted")
		}
		time.Sleep(10 * time.Millisecond)
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
