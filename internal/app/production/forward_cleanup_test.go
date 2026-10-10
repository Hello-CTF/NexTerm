//go:build darwin || linux

package production

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type fakeDialTransport struct {
	fakeSSHTransport
	upstream string
}

func (t *fakeDialTransport) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", t.upstream)
}

func startEchoListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func newForwardTestProduction(t *testing.T, upstream string) *Production {
	t.Helper()
	production, err := NewProduction(t.Context(), ProductionConfig{
		Config:  Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		DataDir: t.TempDir(),
		Desktop: true,
		Connector: session.ConnectorFunc(func(_ context.Context, asset session.Asset, generation uint64) (base.Transport, error) {
			return &fakeDialTransport{
				fakeSSHTransport: fakeSSHTransport{kind: asset.Kind, generation: generation},
				upstream:         upstream,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := production.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = production.Shutdown(context.Background()) })
	return production
}

func connectForwardSession(t *testing.T, production *Production) string {
	t.Helper()
	connected, err := production.Services.Sessions.Connect(t.Context(), session.Asset{ID: "ssh-asset", Kind: session.KindSSH, Name: "fake-ssh"})
	if err != nil {
		t.Fatal(err)
	}
	return connected.ID
}

func createForward(t *testing.T, production *Production, sessionID string) forward.Spec {
	t.Helper()
	spec, err := production.Services.Forward.CreateLocal(t.Context(), forward.CreateLocalArgs{
		SessionID: sessionID, TargetHost: "remote.internal", TargetPort: 22,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func assertForwardEcho(t *testing.T, spec forward.Spec, payload string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != payload {
		t.Fatalf("echo = %q, want %q", response, payload)
	}
	return conn
}

func TestForwardStoppedOnSessionDisconnect(t *testing.T) {
	production := newForwardTestProduction(t, startEchoListener(t))
	sessionID := connectForwardSession(t, production)
	spec := createForward(t, production, sessionID)
	conn := assertForwardEcho(t, spec, "before-disconnect")
	defer conn.Close()

	if err := production.Services.Sessions.Disconnect(sessionID); err != nil {
		t.Fatal(err)
	}
	if got := production.Services.Forward.List(); len(got) != 0 {
		t.Fatalf("forwards after disconnect = %+v", got)
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.ListenPort))), 100*time.Millisecond); err == nil {
		t.Fatal("forward listener still accepts after session disconnect")
	}
}

func TestForwardSurvivesSessionReconnect(t *testing.T) {
	production := newForwardTestProduction(t, startEchoListener(t))
	sessionID := connectForwardSession(t, production)
	spec := createForward(t, production, sessionID)

	if err := production.Services.Sessions.Reconnect(t.Context(), sessionID); err != nil {
		t.Fatal(err)
	}
	remaining := production.Services.Forward.List()
	if len(remaining) != 1 || remaining[0].ID != spec.ID {
		t.Fatalf("forwards after reconnect = %+v", remaining)
	}
	conn := assertForwardEcho(t, spec, "after-reconnect")
	_ = conn.Close()
}
