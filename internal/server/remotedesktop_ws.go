package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/remotedesktop"
	"github.com/coder/websocket"
)

// RemoteDesktopServer terminates a remote desktop frame transport (internal/remotedesktop.Service).
type RemoteDesktopServer interface {
	Serve(ctx context.Context, conn remotedesktop.FrameConn, identity remotedesktop.Identity, ticket string) error
}

const (
	// remoteDesktopReadLimit bounds one client message; IronRDP clipboard and input PDUs stay far below it.
	remoteDesktopReadLimit = 4 << 20
	// remoteDesktopWriteTimeout lets a slow browser apply backpressure without dropping desktop data.
	remoteDesktopWriteTimeout = 30 * time.Second
)

// wsFrameConn adapts a websocket to remotedesktop.FrameConn. Reads accept only binary messages; no per-message
// compression is negotiated (frames are already compressed or encrypted).
type wsFrameConn struct {
	conn         *websocket.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	writeTimeout time.Duration
	reads        int
}

func (c *wsFrameConn) ReadFrame() ([]byte, error) {
	// The first message only carries a ticket or an RDCleanPath request, so it gets the small limit before it is read.
	if c.reads == 0 {
		c.conn.SetReadLimit(remotedesktop.FirstFrameLimit)
	} else if c.reads == 1 {
		c.conn.SetReadLimit(remoteDesktopReadLimit)
	}
	c.reads++
	for {
		kind, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return nil, err
		}
		if kind == websocket.MessageBinary {
			return data, nil
		}
	}
}

func (c *wsFrameConn) WriteFrame(data []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, c.writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageBinary, data)
}

func (c *wsFrameConn) Close() error {
	c.cancel()
	return c.conn.CloseNow()
}

// serveRemoteDesktop handles GET /ws/remotedesktop?ticket=... . The ticket may instead arrive in the first frame
// (a ticket preamble or the RDCleanPath proxy_auth), which is how IronRDP carries it.
func (s *Server) serveRemoteDesktop(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.admitWebSocket(w, r)
	if !ok {
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.options.AllowedOrigins})
	if err != nil {
		return
	}
	ctx, release, err := s.sockets.track(r.Context(), connection)
	if err != nil {
		_ = connection.CloseNow()
		return
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	frames := &wsFrameConn{conn: connection, ctx: ctx, cancel: cancel, writeTimeout: remoteDesktopWriteTimeout}
	defer frames.Close()
	if s.webSocket.KeepAlive > 0 {
		go func() {
			ticker := time.NewTicker(s.webSocket.KeepAlive)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				pingCtx, pingCancel := context.WithTimeout(ctx, s.webSocket.PingTimeout)
				err := connection.Ping(pingCtx)
				pingCancel()
				if err != nil {
					cancel()
					_ = connection.CloseNow()
					return
				}
			}
		}()
	}
	var ident remotedesktop.Identity
	if identity != nil {
		ident = remotedesktop.Identity{UserID: identity.UserID, LoginSessionID: identity.SessionID}
	}
	if err := s.remoteDesktop.Serve(ctx, frames, ident, r.URL.Query().Get("ticket")); err != nil {
		s.logger.Info("remote desktop connection ended", "error", ipc.NormalizeError(err).Message)
	}
}
