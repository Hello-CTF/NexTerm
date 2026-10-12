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
	// remoteFeedReadLimit 是推流 WS 单条消息读上限: 输出帧是一帧一喂, 上限只
	// 兜住异常大消息; 缓冲边界另有 hub 订阅队列 (512 帧/16MiB, 背压阻塞) 与
	// 终端模型滚动环兜底。
	remoteFeedReadLimit = 1 << 20
)

// RemoteSessionRegistry 是远程会话壳对 HTTP 层暴露的管理面: 壳注册、推流、
// 结束、归属查询。session.Manager 实现它; 装配层把同一个 Manager 同时注入
// Config.Spectator, 服务端据此挂载 /share/remote-sessions 一组端点。
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

// serveRemoteSessionCreate 处理 POST /share/remote-sessions: 为当前账号注册一个
// 远程会话壳 (无本地进程的服务端终端标签页), 返回的 session_id 可直接用于
// POST /share/previews 建只读预览, 也是推流/删除路径里的 {id}。
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

// remoteFeedControlFrame 是推流 WS 上唯一的文本帧形态 (控制帧); 输出数据走二
// 进制帧, 字节原样进入终端模型, 与 hub 内部二进制输出帧一致。
type remoteFeedControlFrame struct {
	Type     string `json:"type"`
	ExitCode *int   `json:"exit_code"`
	Cols     uint32 `json:"cols"`
	Rows     uint32 `json:"rows"`
}

// serveRemoteSessionFeed 处理 GET /share/remote-sessions/{id}/feed (WS): 桌面端
// 把本地终端输出推进远程壳。仅壳的创建者可推; 同一壳只允许一条活跃推流连接。
// 连接是单向的: 服务端只读不推 (除终止前的错误帧); 断线保留壳可重连续推,
// end 控制帧按进程退出语义关闭写入方向 (快照保留)。
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

// pumpRemoteFeed 是推流读循环: 二进制帧 = 输出数据 (原样喂壳), 文本帧 = 控制
// 帧 (end/resize); 任何读错误即断开 (壳保留), 协议错误先下发错误帧再关闭。
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

// remoteFeedTermination 把推流错误映射为可下发给推流方的终止码; 内部错误不
// 携带细节 (避免泄露内部状态)。
func remoteFeedTermination(err error) (string, string) {
	switch {
	case errors.Is(err, session.ErrTabNotFound), errors.Is(err, session.ErrTabClosed):
		return "ended", "远程会话不存在或已结束"
	case errors.Is(err, session.ErrInvalidSize):
		return "bad_param", "终端尺寸参数无效"
	}
	return "internal", "推流暂时不可用"
}

// serveRemoteSessionDelete 处理 DELETE /share/remote-sessions/{id}: 显式终结
// 远程壳, 会话连同标签页从 hub 移除, 其上的只读预览流随之终止。仅创建者可删。
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
