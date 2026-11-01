package main

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/remotedesktop"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// wailsStreamConn is the part of *application.StreamConn the remote desktop adapter uses.
type wailsStreamConn interface {
	Receive() ([]byte, error)
	Send([]byte) error
	Close() error
	Context() context.Context
}

// streamFrameConn adapts a Wails GoStream connection to remotedesktop.FrameConn. Send blocks while the window
// drains its queue, so a slow WebView applies backpressure instead of losing desktop data.
type streamFrameConn struct{ conn wailsStreamConn }

func (c streamFrameConn) ReadFrame() ([]byte, error) { return c.conn.Receive() }
func (c streamFrameConn) WriteFrame(data []byte) error {
	return c.conn.Send(data)
}
func (c streamFrameConn) Close() error {
	return c.conn.Close()
}

type remoteDesktopServer interface {
	Serve(ctx context.Context, conn remotedesktop.FrameConn, identity remotedesktop.Identity, ticket string) error
}

// remoteDesktopStreamHandler serves one remotedesktop stream connection. The desktop and mobile shells run a single
// local user, so the identity is empty and the ticket arrives in the first frame (TicketPreamble+ticket, or the
// RDCleanPath request whose proxy_auth carries it).
func remoteDesktopStreamHandler(service remoteDesktopServer) application.StreamHandler {
	return func(conn *application.StreamConn) {
		serveRemoteDesktopStream(service, conn)
	}
}

func serveRemoteDesktopStream(service remoteDesktopServer, conn wailsStreamConn) {
	// The service audits and reports failures to the peer; the stream closes when the handler returns.
	_ = service.Serve(conn.Context(), streamFrameConn{conn: conn}, remotedesktop.Identity{}, "")
}

func registerRemoteDesktopStream(app *application.App, service remoteDesktopServer) {
	if app == nil || service == nil {
		return
	}
	app.HandleStream(remotedesktop.StreamName, remoteDesktopStreamHandler(service))
}
