package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/remotedesktop"
	"github.com/coder/websocket"
)

type recordingRemoteDesktop struct {
	tickets chan string
	idents  chan remotedesktop.Identity
}

func (r *recordingRemoteDesktop) Serve(ctx context.Context, conn remotedesktop.FrameConn, ident remotedesktop.Identity, ticket string) error {
	r.tickets <- ticket
	r.idents <- ident
	for {
		frame, err := conn.ReadFrame()
		if err != nil {
			return err
		}
		if err := conn.WriteFrame(append([]byte("echo:"), frame...)); err != nil {
			return err
		}
	}
}

func TestRemoteDesktopWebSocketCarriesBinaryFramesAndTicket(t *testing.T) {
	handler := &recordingRemoteDesktop{tickets: make(chan string, 1), idents: make(chan remotedesktop.Identity, 1)}
	config := testConfig(t, false)
	config.RemoteDesktop = handler
	_, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/remotedesktop?ticket=abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if got := <-handler.tickets; got != "abc" {
		t.Fatalf("ticket = %q", got)
	}
	if err := connection.Write(ctx, websocket.MessageBinary, []byte{0, 1, 2}); err != nil {
		t.Fatal(err)
	}
	kind, data, err := connection.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || string(data) != "echo:\x00\x01\x02" {
		t.Fatalf("frame = %v %q %v", kind, data, err)
	}
	// No terminal compression context is negotiated for desktop frames.
	if err := connection.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDesktopWebSocketRejectsForeignOrigin(t *testing.T) {
	handler := &recordingRemoteDesktop{tickets: make(chan string, 1), idents: make(chan remotedesktop.Identity, 1)}
	config := testConfig(t, false)
	config.RemoteDesktop = handler
	_, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, response, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/remotedesktop?ticket=abc", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatal("cross-origin websocket accepted")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %+v, err = %v", response, err)
	}
	select {
	case <-handler.tickets:
		t.Fatal("handler ran for a rejected origin")
	default:
	}
}

func TestRemoteDesktopWebSocketFirstFrameLimit(t *testing.T) {
	handler := &recordingRemoteDesktop{tickets: make(chan string, 1), idents: make(chan remotedesktop.Identity, 1)}
	config := testConfig(t, false)
	config.RemoteDesktop = handler
	_, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/remotedesktop?ticket=abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	<-handler.tickets
	<-handler.idents
	connection.SetReadLimit(-1)
	_ = connection.Write(ctx, websocket.MessageBinary, make([]byte, remotedesktop.FirstFrameLimit+1))
	if _, _, err := connection.Read(ctx); err == nil {
		t.Fatal("oversized first frame was accepted")
	}

	// Later frames may use the larger limit.
	second, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http", "ws", 1)+"/ws/remotedesktop?ticket=abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.CloseNow()
	<-handler.tickets
	<-handler.idents
	second.SetReadLimit(-1)
	if err := second.Write(ctx, websocket.MessageBinary, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.Write(ctx, websocket.MessageBinary, make([]byte, remotedesktop.FirstFrameLimit*2)); err != nil {
		t.Fatal(err)
	}
	if _, data, err := second.Read(ctx); err != nil || len(data) != remotedesktop.FirstFrameLimit*2+5 {
		t.Fatalf("large later frame: %d bytes, %v", len(data), err)
	}
}

type userCloseRecorder struct {
	user, except string
	calls        int
}

func (r *userCloseRecorder) CloseAccountSession(string) error { return nil }
func (r *userCloseRecorder) CloseAccountUser(user, except string) {
	r.user, r.except = user, except
	r.calls++
}

func TestCloseUserConnectionsReachesServicesThatSupportIt(t *testing.T) {
	recorder := &userCloseRecorder{}
	(&Server{accountConnections: recorder}).closeUserConnections("u1", "keep")
	if recorder.calls != 1 || recorder.user != "u1" || recorder.except != "keep" {
		t.Fatalf("recorder = %+v", recorder)
	}
	(&Server{}).closeUserConnections("u1", "") // no closer configured
}
