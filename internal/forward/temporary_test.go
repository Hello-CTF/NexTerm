package forward

import (
	"net"
	"strconv"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func newServerModeService(t *testing.T, provider DialerProvider) *Service {
	t.Helper()
	service := NewService(Config{Provider: provider, Policy: Policy{Desktop: false, Platform: "other"}})
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestTemporaryForwardBindsLoopbackInServerMode(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newServerModeService(t, &switchProvider{dialer: dialer})
	if service.Environment().ListenHost != "0.0.0.0" {
		t.Fatalf("server mode listen host = %q", service.Environment().ListenHost)
	}
	spec, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "db.internal", TargetPort: 5432, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Temporary || spec.ListenHost != "127.0.0.1" || spec.ListenPort == 0 {
		t.Fatalf("spec = %+v", spec)
	}
	conn := dialForward(t, spec)
	assertEcho(t, conn, "temporary")
	ordinary, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "db.internal", TargetPort: 5432})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Temporary || ordinary.ListenHost != "0.0.0.0" {
		t.Fatalf("ordinary spec = %+v", ordinary)
	}
}

func TestFailSessionRemovesTemporaryAndKeepsOrdinary(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	temp, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "h", TargetPort: 1, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := service.CreateLocal(t.Context(), CreateLocalArgs{SessionID: "s", TargetHost: "h", TargetPort: 2})
	if err != nil {
		t.Fatal(err)
	}
	service.FailSession("s", "disconnected")
	specs := service.List()
	if len(specs) != 1 || specs[0].ID != ordinary.ID || specs[0].Status != StatusFailed {
		t.Fatalf("specs after FailSession = %+v", specs)
	}
	if conn, err := net.Dial("tcp", net.JoinHostPort(temp.ListenHost, itoa(temp.ListenPort))); err == nil {
		_ = conn.Close()
		t.Fatal("temporary listener still accepts connections")
	}
}

func TestTemporaryForwardIsOwnerIsolatedAndNotRetryable(t *testing.T) {
	dialer := &recordingDialer{upstream: startEchoServer(t)}
	service := newLoopbackService(t, &switchProvider{dialer: dialer})
	ctxA := ipc.WithUserID(t.Context(), "owner-a")
	ctxB := ipc.WithUserID(t.Context(), "owner-b")
	spec, err := service.CreateLocal(ctxA, CreateLocalArgs{SessionID: "s", TargetHost: "h", TargetPort: 1, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := service.ListFor(ctxB); len(got) != 0 {
		t.Fatalf("owner B sees %+v", got)
	}
	if got := service.ListFor(ctxA); len(got) != 1 || got[0].ID != spec.ID {
		t.Fatalf("owner A sees %+v", got)
	}
	if err := service.RemoveFor(ctxB, spec.ID); err == nil {
		t.Fatal("owner B removed owner A's temporary forward")
	}
	if got := service.ListFor(ctxA); len(got) != 1 {
		t.Fatalf("forward vanished after foreign remove: %+v", got)
	}
	if _, err := service.RetryFor(ctxA, spec.ID, ""); err == nil {
		t.Fatal("retry of a temporary forward must fail")
	}
	if err := service.RemoveFor(ctxA, spec.ID); err != nil {
		t.Fatal(err)
	}
	if got := service.ListFor(ctxA); len(got) != 0 {
		t.Fatalf("after remove: %+v", got)
	}
}

func itoa(port uint16) string { return strconv.Itoa(int(port)) }
