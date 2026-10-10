package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/coder/websocket"
)

const (
	// previewViewerReadLimit 是预览 viewer WS 的读上限: 合同没有输入帧, 读只
	// 为了应答控制帧与观察断开, 这里只兜住异常大消息。
	previewViewerReadLimit = 1 << 20
	// defaultPreviewRevalidateInterval 是预览连接的授权复查间隔: 无论有无
	// 输出, 吊销/过期最迟在该间隔内生效。
	defaultPreviewRevalidateInterval = 10 * time.Second
)

// PreviewSpectator 是只读预览对会话 Manager 的最小依赖: 订阅标签页输出、
// 查归属、断开清理。
type PreviewSpectator interface {
	AttachSpectator(context.Context, string, session.SpectateOptions) (session.TabInfo, error)
	TabOwner(string) (string, error)
	DetachChannel(string) error
}

type previewLinkView struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	SessionID      string `json:"session_id"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
}

func newPreviewLinkView(link *sharing.PreviewLink) previewLinkView {
	return previewLinkView{
		ID: link.ID, OwnerID: link.OwnerID, SessionID: link.SessionID,
		CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt,
		RevokedAt: link.RevokedAt, LastAccessedAt: link.LastAccessedAt,
	}
}

type previewLinkCreateRequest struct {
	SessionID string `json:"session_id"`
	TTLMS     int64  `json:"ttl_ms"`
}

// servePreviewLinkCreate 处理 POST /share/previews: 为服务端托管的终端标签页
// 创建只读预览链接。会话有主时仅本人或超管, 无主会话 (后台/桌面来源) 仅超管;
// 创建响应是唯一携带一次性 token 的入口。
func (s *Server) servePreviewLinkCreate(w http.ResponseWriter, r *http.Request) {
	var request previewLinkCreateRequest
	if err := decodeAccountJSON(w, r, &request); err != nil {
		writeAccountFailure(w, err)
		return
	}
	identity := accountIdentityFrom(r.Context())
	owner, err := s.spectator.TabOwner(request.SessionID)
	switch {
	case errors.Is(err, session.ErrTabNotFound):
		writeAccountError(w, http.StatusNotFound, ipc.NewError(ipc.CodeNotFound, "终端标签页不存在或已关闭"))
		return
	case err != nil:
		writeAccountFailure(w, err)
		return
	}
	if identity.Role != account.RoleSuperadmin && owner != identity.UserID {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "终端不属于该用户"))
		return
	}
	link, token, err := s.previews.CreatePreviewLink(r.Context(), identity, request.SessionID, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, struct {
		previewLinkView
		Token string `json:"token"`
	}{previewLinkView: newPreviewLinkView(link), Token: token})
}

func (s *Server) servePreviewLinkList(w http.ResponseWriter, r *http.Request) {
	links, err := s.previews.ListPreviewLinks(r.Context(), accountIdentityFrom(r.Context()))
	if err != nil {
		writeAccountFailure(w, err)
		return
	}
	views := make([]previewLinkView, 0, len(links))
	for _, link := range links {
		views = append(views, newPreviewLinkView(link))
	}
	writeAccountJSON(w, http.StatusOK, map[string][]previewLinkView{"links": views})
}

func (s *Server) servePreviewLinkRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.previews.RevokePreviewLink(r.Context(), accountIdentityFrom(r.Context()), r.PathValue("id")); err != nil {
		writeAccountFailure(w, err)
		return
	}
	writeAccountJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type previewReadyFrame struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Permission string `json:"permission"`
	ExpiresAt  int64  `json:"expires_at"`
}

type previewErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// serveSharePreview 处理 GET /share/preview/{token}: 浏览器普通导航由静态入口
// 直接服务既有 SPA (不校验 token, 一律 no-store); WS upgrade 才是数据面 —
// 匿名 viewer 用公开 token 换一次性只读授权 (ResolvePreview 已审计), 随后
// AttachSpectator 订阅标签页: 首帧是当前屏幕快照, 之后只有实时输出。合同里
// 没有输入帧类型, viewer 的任何上行字节都被读循环丢弃, 输入在协议层即关闭。
// 授权按间隔复查 (无论有无输出), 吊销/过期即终止。
func (s *Server) serveSharePreview(page http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isPreviewWSUpgrade(r) {
			servePreviewPage(page, w, r)
			return
		}
		key := "preview-resolve|" + clientIP(r)
		if !s.previewResolve.Allow(key) {
			writeAccountJSON(w, http.StatusTooManyRequests, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试")))
			return
		}
		grant, err := s.previews.ResolvePreview(r.Context(), r.PathValue("token"))
		if err == nil {
			s.previewResolve.RecordSuccess(key)
		} else if errors.Is(err, sharing.ErrTokenNotFound) {
			// 只对 token 不存在计入限流退避 (随机 token 洪水); 吊销/过期等真实
			// token 的拒绝不惩罚持有者。
			s.previewResolve.RecordFailure(key)
		}
		if err != nil {
			writeAccountFailure(w, err)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(previewViewerReadLimit)
		ctx, release, err := s.sockets.track(r.Context())
		if err != nil {
			_ = conn.CloseNow()
			return
		}
		defer release()
		channelID := ids.New()
		if _, err := s.spectator.AttachSpectator(ctx, grant.SessionID, session.SpectateOptions{
			ChannelID: channelID,
			ClientID:  "preview-" + grant.ShareID,
		}); err != nil {
			s.failSharePreview(conn, err)
			return
		}
		receiver, err := s.channels.BindChannel(channelID)
		if err != nil {
			_ = s.spectator.DetachChannel(channelID)
			s.failSharePreview(conn, err)
			return
		}
		defer receiver.Close()
		if err := writePreviewJSON(ctx, conn, previewReadyFrame{
			Type: "ready", SessionID: grant.SessionID,
			Permission: string(sharing.PermissionRead), ExpiresAt: grant.ExpiresAt,
		}); err != nil {
			return
		}
		s.pumpSharePreview(ctx, conn, receiver, grant)
	}
}

// pumpSharePreview 把订阅帧泵给 viewer 直到任一侧结束或授权失效。复查在每帧
// 写出前与空闲超时两边都触发, 吊销/过期不依赖流量。终止前先下发可识别错误帧
// (作为正常帧走 pumpSocket 的串行写路径, 不与 keepalive ping 竞争写者)。
func (s *Server) pumpSharePreview(ctx context.Context, conn *websocket.Conn, receiver ChannelReceiver, grant *sharing.PreviewGrant) {
	interval := s.previewRevalidateInterval
	if interval <= 0 {
		interval = defaultPreviewRevalidateInterval
	}
	lastCheck := time.Now()
	var terminalErr error
	s.pumpSocket(ctx, conn, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		if terminalErr != nil {
			return 0, nil, nil, terminalErr
		}
		for {
			if time.Since(lastCheck) >= interval {
				refreshed, err := s.previews.RevalidatePreview(ctx, grant)
				if err != nil {
					terminalErr = err
					code, message := previewTermination(err)
					payload, marshalErr := json.Marshal(previewErrorFrame{Type: "error", Code: code, Message: message})
					if marshalErr != nil {
						return 0, nil, nil, terminalErr
					}
					return websocket.MessageText, payload, nil, nil
				}
				grant = refreshed
				lastCheck = time.Now()
			}
			frameCtx, cancel := context.WithTimeout(ctx, interval)
			frame, err := receiver.Next(frameCtx)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if err != nil {
				code, message := previewTermination(err)
				terminalErr = err
				payload, marshalErr := json.Marshal(previewErrorFrame{Type: "error", Code: code, Message: message})
				if marshalErr != nil {
					return 0, nil, nil, terminalErr
				}
				return websocket.MessageText, payload, nil, nil
			}
			ack := func() { _ = receiver.Ack(frame.Sequence) }
			switch frame.Kind {
			case FrameBinary:
				return websocket.MessageBinary, frame.Data, ack, nil
			case FrameJSON:
				return websocket.MessageText, frame.Data, ack, nil
			default:
				return 0, nil, nil, errors.New("unknown channel frame kind")
			}
		}
	})
}

func (s *Server) failSharePreview(conn *websocket.Conn, err error) {
	code, message := previewTermination(err)
	_ = writePreviewJSON(context.Background(), conn, previewErrorFrame{Type: "error", Code: code, Message: message})
	_ = conn.Close(websocket.StatusPolicyViolation, "preview failed")
}

func writePreviewJSON(ctx context.Context, conn *websocket.Conn, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload)
}

// previewTermination 把泵错误映射为可安全下发给 viewer 的终止码; 传输层断开
// 与内部错误不携带细节 (避免泄露内部状态)。
func previewTermination(err error) (string, string) {
	switch {
	case errors.Is(err, sharing.ErrPreviewExpired):
		return "expired", "分享链接已过期"
	case errors.Is(err, sharing.ErrPreviewRevoked):
		return "revoked", "分享链接已吊销"
	case errors.Is(err, sharing.ErrPreviewInvalid), errors.Is(err, sharing.ErrTokenNotFound):
		return "not_found", "分享链接无效"
	case errors.Is(err, session.ErrTabNotFound), errors.Is(err, session.ErrTabClosed):
		return "not_found", "会话不存在或已关闭"
	case errors.Is(err, hub.ErrClosed), errors.Is(err, hub.ErrDetached):
		return "ended", "分享已结束"
	}
	return "internal", "分享暂时不可用"
}

// servePreviewPage 用静态入口服务既有 SPA (与 "/" 同一 index.html, 不重定向
// 不另造 shell); token URL 一律 no-store, 路径固定为 "/" 再做文件查找。
func servePreviewPage(page http.Handler, w http.ResponseWriter, r *http.Request) {
	if page == nil {
		http.NotFound(w, r)
		return
	}
	cloned := r.Clone(r.Context())
	cloned.URL.Path = "/"
	cloned.URL.RawPath = ""
	cloned.RequestURI = "/"
	if cloned.URL.RawQuery != "" {
		cloned.RequestURI += "?" + cloned.URL.RawQuery
	}
	page.ServeHTTP(previewNoStoreWriter{w}, cloned)
}

// previewNoStoreWriter 在写出前强制 Cache-Control: no-store, 覆盖静态入口对
// text/html 默认的 no-cache — token URL 不允许任何缓存。
type previewNoStoreWriter struct{ http.ResponseWriter }

func (w previewNoStoreWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}

func (w previewNoStoreWriter) Write(payload []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(payload)
}

// isPreviewWSUpgrade 判定请求是否为 WebSocket 升级; 判定条件与
// websocket.Accept 的握手校验一致 (Connection/Upgrade 头 token, 大小写不敏感)。
func isPreviewWSUpgrade(r *http.Request) bool {
	return previewHeaderHasToken(r.Header, "Connection", "upgrade") && previewHeaderHasToken(r.Header, "Upgrade", "websocket")
}

func previewHeaderHasToken(header http.Header, key, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
