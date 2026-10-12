package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/coder/websocket"
)

const (
	// previewViewerReadLimit 是预览 viewer WS 的读上限: 上行只有 read_write
	// 链接的输入帧 (人类击键量级), 这里只兜住异常大消息。
	previewViewerReadLimit = 1 << 20
	// defaultPreviewRevalidateInterval 是预览连接的授权复查间隔: 无论有无
	// 输出, 吊销/过期最迟在该间隔内生效。
	defaultPreviewRevalidateInterval = 10 * time.Second
	// previewGuestLease 是预览访客的控制租约时长: 每次放行写入即续租,
	// WS 结束时 ReleaseGuest 显式释放, ready 帧以 lease_ms 告知 viewer。
	previewGuestLease = 3 * time.Second
)

// PreviewSpectator 是预览对会话 Manager 的最小依赖: 订阅标签页输出、查归属、
// 断开清理, 以及 read_write 链接的访客写入仲裁 (WriteGuest/ReleaseGuest)。
type PreviewSpectator interface {
	AttachSpectator(context.Context, string, session.SpectateOptions) (session.TabInfo, error)
	TabOwner(string) (string, error)
	DetachChannel(string) error
	WriteGuest(context.Context, string, string, []byte, time.Duration) error
	ReleaseGuest(string, string) error
}

type previewLinkView struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	SessionID      string `json:"session_id"`
	Permission     string `json:"permission"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
}

func newPreviewLinkView(link *sharing.PreviewLink) previewLinkView {
	return previewLinkView{
		ID: link.ID, OwnerID: link.OwnerID, SessionID: link.SessionID,
		Permission: string(link.Permission),
		CreatedAt:  link.CreatedAt, ExpiresAt: link.ExpiresAt,
		RevokedAt: link.RevokedAt, LastAccessedAt: link.LastAccessedAt,
	}
}

type previewLinkCreateRequest struct {
	SessionID string `json:"session_id"`
	TTLMS     int64  `json:"ttl_ms"`
	Write     bool   `json:"write"`
}

// servePreviewLinkCreate 处理 POST /share/previews: 为服务端托管的终端标签页
// 创建预览链接, 默认只读, write 显式为 true 时授予读写。仅会话所有者可创建;
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
	if owner != identity.UserID {
		writeAccountError(w, http.StatusForbidden, ipc.NewError(ipc.CodeForbidden, "终端不属于该用户"))
		return
	}
	link, token, err := s.previews.CreatePreviewLink(r.Context(), identity, request.SessionID, request.Write, time.Duration(request.TTLMS)*time.Millisecond)
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
	LeaseMS    int64  `json:"lease_ms"`
}

type previewErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// previewControlFrame 是 server→viewer 的控制帧: 反馈一次访客写入的仲裁结果。
// guest 携带租约到期时刻 (epoch ms); owner/busy 不携带。
type previewControlFrame struct {
	Type       string `json:"type"`
	State      string `json:"state"`
	LeaseUntil int64  `json:"lease_until,omitempty"`
}

// serveSharePreview 处理 GET /share/preview/{token}: 浏览器普通导航由静态入口
// 直接服务既有 SPA (不校验 token, 一律 no-store); WS upgrade 才是数据面 —
// 匿名 viewer 用公开 token 换一次性授权 (ResolvePreview 已审计), 随后
// AttachSpectator 订阅标签页: 首帧是当前屏幕快照, 之后只有实时输出。只读链接
// 不安装输入处理, viewer 的上行字节被读循环丢弃; read_write 链接的 viewer 输入
// 是二进制帧, 每次输入经 RevalidatePreviewInput 按当前行重查 (吊销/过期/权限
// 收缩即时生效) 后进入 WriteGuest 租约仲裁, 仲裁结果以 control 帧反馈, 授权拒
// 绝按语义下非终止错误帧 (降级 read_only / 吊销 revoked / 过期 expired);
// read_write 连接的 WS 结束即 ReleaseGuest 释放控制权与租约。输出侧授权按间隔
// 复查 (无论有无输出), 吊销/过期即终止。
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
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
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
		queue := &previewSendQueue{}
		var onRead func(context.Context, websocket.MessageType, []byte)
		if grant.Permission.AllowsWrite() {
			input := &previewInputHandler{s: s, grant: grant, guest: "preview-" + grant.ShareID, queue: queue}
			onRead = input.handle
			defer func() { _ = s.spectator.ReleaseGuest(grant.SessionID, input.guest) }()
		}
		if err := writePreviewJSON(ctx, conn, previewReadyFrame{
			Type: "ready", SessionID: grant.SessionID,
			Permission: string(grant.Permission), ExpiresAt: grant.ExpiresAt,
			LeaseMS: int64(previewGuestLease / time.Millisecond),
		}); err != nil {
			return
		}
		s.pumpSharePreview(ctx, conn, receiver, grant, queue, onRead)
	}
}

// pumpSharePreview 把订阅帧泵给 viewer 直到任一侧结束或授权失效。复查在每帧
// 写出前与空闲超时两边都触发, 吊销/过期不依赖流量。终止前先下发可识别错误帧
// (作为正常帧走 pumpSocket 的串行写路径, 不与 keepalive ping 竞争写者)。
// queue 里的 control/read_only 帧同样走该写路径, 与输出帧按到达顺序交错;
// onRead 透传给 pumpSocket 的读循环 (只读链接为 nil)。
func (s *Server) pumpSharePreview(ctx context.Context, conn *websocket.Conn, receiver ChannelReceiver, grant *sharing.PreviewGrant, queue *previewSendQueue, onRead func(context.Context, websocket.MessageType, []byte)) {
	interval := s.previewRevalidateInterval
	if interval <= 0 {
		interval = defaultPreviewRevalidateInterval
	}
	lastCheck := time.Now()
	var terminalErr error
	s.pumpSocketWithRead(ctx, conn, func(ctx context.Context) (websocket.MessageType, []byte, func(), error) {
		if terminalErr != nil {
			return 0, nil, nil, terminalErr
		}
		for {
			if payload := queue.pop(); payload != nil {
				return websocket.MessageText, payload, nil, nil
			}
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
			queue.arm(cancel)
			frame, err := receiver.Next(frameCtx)
			queue.disarm()
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if errors.Is(err, context.Canceled) && ctx.Err() == nil {
				// 被 queue 的待写帧唤醒, 回循环顶取帧。
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
	}, onRead)
}

// previewSendQueue 是 viewer 上行处理 (读 goroutine) 向泵写循环投递文本帧的
// 队列: control 与 read_only 帧都经此走 pumpSocket 的串行写路径。push 唤醒正在
// 空闲等待的 receiver.Next (arm 注册的 cancel), 避免控制反馈睡满复查间隔。
type previewSendQueue struct {
	mu     sync.Mutex
	frames [][]byte
	wake   context.CancelFunc
}

func (q *previewSendQueue) pushJSON(frame any) {
	payload, err := json.Marshal(frame)
	if err != nil {
		return
	}
	q.mu.Lock()
	q.frames = append(q.frames, payload)
	wake := q.wake
	q.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (q *previewSendQueue) pop() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == 0 {
		return nil
	}
	payload := q.frames[0]
	q.frames = q.frames[1:]
	return payload
}

func (q *previewSendQueue) arm(cancel context.CancelFunc) {
	q.mu.Lock()
	q.wake = cancel
	pending := len(q.frames) > 0
	q.mu.Unlock()
	if pending {
		cancel()
	}
}

func (q *previewSendQueue) disarm() {
	q.mu.Lock()
	q.wake = nil
	q.mu.Unlock()
}

// previewInputHandler 是 read_write 预览连接的输入路径: 只接受二进制帧 (文本
// 与未知帧静默丢弃), 每次输入经 RevalidatePreviewInput 按当前行重查授权, 放行
// 后进入 WriteGuest 租约仲裁, 仲裁结果以 control 帧反馈; 授权拒绝按语义下非终
// 止错误帧 (见 rejectInput), 吊销/过期的连接终止由输出侧周期复查负责。handle
// 只在 pumpSocket 读 goroutine 内运行, grant 快照无需加锁。
type previewInputHandler struct {
	s     *Server
	grant *sharing.PreviewGrant
	guest string
	queue *previewSendQueue
}

func (h *previewInputHandler) handle(ctx context.Context, messageType websocket.MessageType, data []byte) {
	if messageType != websocket.MessageBinary {
		return
	}
	grant, err := h.s.previews.RevalidatePreviewInput(ctx, h.grant)
	if err != nil {
		h.rejectInput(err)
		return
	}
	h.grant = grant
	switch err := h.s.spectator.WriteGuest(ctx, grant.SessionID, h.guest, data, previewGuestLease); {
	case err == nil:
		h.queue.pushJSON(previewControlFrame{Type: "control", State: "guest", LeaseUntil: time.Now().Add(previewGuestLease).UnixMilli()})
	case errors.Is(err, session.ErrGuestControlOwner):
		h.queue.pushJSON(previewControlFrame{Type: "control", State: "owner"})
	case errors.Is(err, session.ErrGuestControlBusy):
		h.queue.pushJSON(previewControlFrame{Type: "control", State: "busy"})
	}
	// 其他写入失败 (标签页关闭/断线/不支持访客控制) 静默丢弃: 输出通道很快
	// 会给出终止帧或随重连恢复, 无额外反馈帧类型。
}

// rejectInput 把输入侧授权拒绝映射为非终止错误帧 (连接不断): 吊销/过期与输出侧
// 终止帧同码同文案, 只读降级下 read_only; 链接无效与 DB/内部错误静默, 不泄露
// 内部细节, 不误报 read_only。哨兵之外的 CodeForbidden 即只读降级 —
// RevalidatePreviewInput 唯一的非哨兵 forbidden 错误。
func (h *previewInputHandler) rejectInput(err error) {
	var frame previewErrorFrame
	switch {
	case errors.Is(err, sharing.ErrPreviewRevoked):
		frame = previewErrorFrame{Type: "error", Code: "revoked", Message: "分享链接已吊销"}
	case errors.Is(err, sharing.ErrPreviewExpired):
		frame = previewErrorFrame{Type: "error", Code: "expired", Message: "分享链接已过期"}
	case errors.Is(err, sharing.ErrPreviewInvalid):
		return
	case ipc.NormalizeError(err).Code == ipc.CodeForbidden:
		frame = previewErrorFrame{Type: "error", Code: "read_only", Message: "预览当前为只读"}
	default:
		return
	}
	h.queue.pushJSON(frame)
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
