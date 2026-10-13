package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/coder/websocket"
)

const (
	// remoteFeedReadLimit is the per-message read limit for the feed WS: output frames are fed one at a time, and the limit only
	// catches abnormally large messages; buffer boundaries are additionally backed by the hub subscription queue
	// (512 frames/16MiB, backpressure blocking) and the terminal model's scroll ring.
	remoteFeedReadLimit = 1 << 20
)

// RemoteSessionRegistry is the management surface remote session shells expose to the HTTP layer: shell registration, feeding,
// ending, and ownership lookup. session.Manager implements it; the wiring layer injects the same Manager into
// Config.Spectator, and the server mounts the /share/remote-sessions endpoints accordingly.
type RemoteSessionRegistry interface {
	RegisterRemoteSession(context.Context, session.RemoteSessionOptions) (session.TabInfo, error)
	RemoteSessionInfo(string) (session.TabInfo, error)
	FeedRemote(context.Context, string, []byte) error
	EndRemote(string, *int) error
	ResizeRemote(context.Context, string, uint32, uint32) error
	AttachRemoteFeed(string) (*session.Session, error)
	DetachRemoteFeed(*session.Session)
	CloseRemote(string) error
	TabOwner(string) (string, error)
}

type remoteSessionCreateRequest struct {
	Title   string `json:"title"`
	AssetID string `json:"asset_id"`
	Cols    uint32 `json:"cols"`
	Rows    uint32 `json:"rows"`
}

// serveRemoteSessionCreate handles POST /share/remote-sessions: register a remote session shell (a server-side
// terminal tab with no local process) for the current account; the returned session_id can be used directly for
// POST /share/previews to create a read-only preview, and is also the {id} in the feed/delete paths.
func (s *Server) serveRemoteSessionCreate(w http.ResponseWriter, r *http.Request) {
	var request remoteSessionCreateRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	identity := accountIdentityFrom(r.Context())
	info, err := s.remoteSessions.RegisterRemoteSession(r.Context(), session.RemoteSessionOptions{
		OwnerID: identity.UserID, Title: request.Title, AssetID: request.AssetID,
		Cols: request.Cols, Rows: request.Rows,
	})
	switch {
	case errors.Is(err, session.ErrRemoteLimit):
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "远程会话数量已达上限，请先结束不再使用的会话"))
		return
	case errors.Is(err, session.ErrInvalidSize):
		writeAccountError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeBadParam, "终端尺寸参数无效"))
		return
	case err != nil:
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]any{
		"session_id": info.ID, "cols": info.Cols, "rows": info.Rows,
	})
}

// remoteFeedControlFrame is the only text frame form on the feed WS (control frame); output data goes in binary
// frames, with bytes entering the terminal model verbatim, consistent with hub internal binary output frames.
type remoteFeedControlFrame struct {
	Type     string `json:"type"`
	ExitCode *int   `json:"exit_code"`
	Cols     uint32 `json:"cols"`
	Rows     uint32 `json:"rows"`
}

// serveRemoteSessionFeed handles GET /share/remote-sessions/{id}/feed (WS): the desktop pushes local terminal output
// into the remote shell. Only the shell's creator may feed, and only one active feed connection is allowed per shell.
// The connection is one-way: the server only reads and never pushes (except the error frame before termination); a disconnect
// keeps the shell and can reattach to resume feeding, and the end control frame closes the write direction per process-exit semantics (snapshot preserved).
func (s *Server) serveRemoteSessionFeed(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	tabID := r.PathValue("id")
	owner, err := s.remoteSessions.TabOwner(tabID)
	switch {
	case errors.Is(err, session.ErrTabNotFound):
		writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "远程会话不存在或已关闭"))
		return
	case err != nil:
		writeAccountFailure(w, err)
		return
	}
	if owner != identity.UserID {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "远程会话不属于该用户"))
		return
	}
	if _, err := s.remoteSessions.RemoteSessionInfo(tabID); err != nil {
		switch {
		case errors.Is(err, session.ErrUnsupported):
			writeAccountError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeBadParam, "该终端不是远程会话"))
		case errors.Is(err, session.ErrTabClosed):
			writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "远程会话不存在或已关闭"))
		default:
			writeAccountFailure(w, err)
		}
		return
	}
	feedSession, err := s.remoteSessions.AttachRemoteFeed(tabID)
	switch {
	case errors.Is(err, session.ErrRemoteFeedAttached):
		writeAccountError(w, http.StatusConflict, ipc.NewError(ipc.CodeForbidden, "远程会话已有活跃推流连接"))
		return
	case errors.Is(err, session.ErrTabClosed):
		writeAccountError(w, http.StatusConflict, ipc.NewError(ipc.CodeForbidden, "远程会话已结束"))
		return
	case errors.Is(err, session.ErrTabNotFound):
		writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "远程会话不存在或已关闭"))
		return
	case err != nil:
		writeAccountFailure(w, err)
		return
	}
	defer s.remoteSessions.DetachRemoteFeed(feedSession)
	if !isPreviewWSUpgrade(r) {
		writeAccountError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeBadParam, "该端点需要 WebSocket 升级"))
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	conn.SetReadLimit(remoteFeedReadLimit)
	ctx, release, err := s.sockets.track(r.Context())
	if err != nil {
		_ = conn.CloseNow()
		return
	}
	defer release()
	s.pumpRemoteFeed(ctx, conn, tabID)
}

// pumpRemoteFeed is the feed read loop: binary frames are output data (fed to the shell verbatim), text frames are control
// frames (end/resize); any read error disconnects (the shell is kept), and protocol errors deliver an error frame before closing.
func (s *Server) pumpRemoteFeed(ctx context.Context, conn *websocket.Conn, tabID string) {
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch kind {
		case websocket.MessageBinary:
			if err := s.remoteSessions.FeedRemote(ctx, tabID, data); err != nil {
				s.failRemoteFeed(conn, err)
				return
			}
		case websocket.MessageText:
			var frame remoteFeedControlFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				s.failRemoteFeedWith(conn, "bad_param", "控制帧不是合法 JSON")
				return
			}
			switch frame.Type {
			case "end":
				if err := s.remoteSessions.EndRemote(tabID, frame.ExitCode); err != nil {
					s.failRemoteFeed(conn, err)
					return
				}
				_ = conn.Close(websocket.StatusNormalClosure, "remote session ended")
				return
			case "resize":
				if err := s.remoteSessions.ResizeRemote(ctx, tabID, frame.Cols, frame.Rows); err != nil {
					s.failRemoteFeed(conn, err)
					return
				}
			default:
				s.failRemoteFeedWith(conn, "bad_param", "未知控制帧类型")
				return
			}
		}
	}
}

func (s *Server) failRemoteFeed(conn *websocket.Conn, err error) {
	code, message := remoteFeedTermination(err)
	s.failRemoteFeedWith(conn, code, message)
}

func (s *Server) failRemoteFeedWith(conn *websocket.Conn, code, message string) {
	_ = writePreviewJSON(context.Background(), conn, previewErrorFrame{Type: "error", Code: code, Message: message})
	_ = conn.Close(websocket.StatusPolicyViolation, "remote feed failed")
}

// remoteFeedTermination maps feed errors to termination codes deliverable to the feeder; internal errors carry
// no details (avoiding leaking internal state).
func remoteFeedTermination(err error) (string, string) {
	switch {
	case errors.Is(err, session.ErrTabNotFound), errors.Is(err, session.ErrTabClosed):
		return "ended", "远程会话不存在或已结束"
	case errors.Is(err, session.ErrInvalidSize):
		return "bad_param", "终端尺寸参数无效"
	}
	return "internal", "推流暂时不可用"
}

// serveRemoteSessionDelete handles DELETE /share/remote-sessions/{id}: explicitly end the remote shell,
// removing the session with its tabs from the hub and terminating read-only preview streams on it. Only the creator may delete.
func (s *Server) serveRemoteSessionDelete(w http.ResponseWriter, r *http.Request) {
	identity := accountIdentityFrom(r.Context())
	tabID := r.PathValue("id")
	owner, err := s.remoteSessions.TabOwner(tabID)
	switch {
	case errors.Is(err, session.ErrTabNotFound):
		writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "远程会话不存在或已关闭"))
		return
	case err != nil:
		writeAccountFailure(w, err)
		return
	}
	if owner != identity.UserID {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "远程会话不属于该用户"))
		return
	}
	if err := s.remoteSessions.CloseRemote(tabID); err != nil {
		switch {
		case errors.Is(err, session.ErrUnsupported):
			writeAccountError(w, http.StatusBadRequest, ipc.NewError(ipc.CodeBadParam, "该终端不是远程会话"))
		case errors.Is(err, session.ErrTabNotFound), errors.Is(err, session.ErrTabClosed):
			writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "远程会话不存在或已关闭"))
		default:
			writeAccountFailure(w, err)
		}
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
