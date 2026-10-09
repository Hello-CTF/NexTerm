package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

// maxShareViewerMessage 是 viewer WS 的读上限; 输入帧经 supervisor 协议
// 分片 (单帧上限 256KiB), 这里只兜住异常大消息。
const maxShareViewerMessage = 1 << 20

// defaultShareRevalidateInterval 是空闲分享连接的周期复查间隔: 数据流停止时
// 吊销/过期/设备变化最迟在该间隔内生效。
const defaultShareRevalidateInterval = 10 * time.Second

// shareViewerWriteTimeout 是写 viewer WS 的单条消息超时: 慢 viewer (TCP
// 背压) 不得把输出泵与连接拆除无限期挂死。
const shareViewerWriteTimeout = 30 * time.Second

// shareTerminateCheckTimeout 是终止前强制授权重查的超时: 重查只读数据库,
// 超时后按无终止码处理 (视同纯传输错误), 不拖延连接拆除。
const shareTerminateCheckTimeout = 5 * time.Second

type shareLinkView struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	DeviceID       string `json:"device_id"`
	SessionID      string `json:"session_id"`
	Permission     string `json:"permission"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
}

func newShareLinkView(link *sharing.Link) shareLinkView {
	return shareLinkView{
		ID: link.ID, OwnerID: link.OwnerID, DeviceID: link.DeviceID, SessionID: link.SessionID,
		Permission: string(link.Permission), CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt,
		RevokedAt: link.RevokedAt, LastAccessedAt: link.LastAccessedAt,
	}
}

type hostShareView struct {
	ID                string `json:"id"`
	OwnerID           string `json:"owner_id"`
	OwnerUsername     string `json:"owner_username"`
	DeviceID          string `json:"device_id"`
	RecipientID       string `json:"recipient_id"`
	RecipientUsername string `json:"recipient_username"`
	Permission        string `json:"permission"`
	CreatedAt         int64  `json:"created_at"`
	ExpiresAt         int64  `json:"expires_at"`
	RevokedAt         int64  `json:"revoked_at,omitempty"`
}

func newHostShareView(share *sharing.HostShare) hostShareView {
	return hostShareView{
		ID: share.ID, OwnerID: share.OwnerID, OwnerUsername: share.OwnerUsername,
		DeviceID: share.DeviceID, RecipientID: share.RecipientID, RecipientUsername: share.RecipientUsername,
		Permission: string(share.Permission), CreatedAt: share.CreatedAt, ExpiresAt: share.ExpiresAt, RevokedAt: share.RevokedAt,
	}
}

type shareLinkCreateRequest struct {
	DeviceID  string `json:"device_id"`
	SessionID string `json:"session_id"`
	Write     bool   `json:"write"`
	TTLMS     int64  `json:"ttl_ms"`
}

// deviceShareLinkView 是设备公开链接的管理视图; 与 shareLinkView 的区别是
// 绑定设备而非单个会话, 并携带 not_before (延迟生效时刻)。
type deviceShareLinkView struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	DeviceID       string `json:"device_id"`
	Permission     string `json:"permission"`
	CreatedAt      int64  `json:"created_at"`
	NotBefore      int64  `json:"not_before"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
}

func newDeviceShareLinkView(link *sharing.DeviceLink) deviceShareLinkView {
	return deviceShareLinkView{
		ID: link.ID, OwnerID: link.OwnerID, DeviceID: link.DeviceID,
		Permission: string(link.Permission), CreatedAt: link.CreatedAt, NotBefore: link.NotBefore, ExpiresAt: link.ExpiresAt,
		RevokedAt: link.RevokedAt, LastAccessedAt: link.LastAccessedAt,
	}
}

type deviceShareLinkCreateRequest struct {
	DeviceID    string `json:"device_id"`
	Write       bool   `json:"write"`
	NotBeforeMS int64  `json:"not_before_ms"`
	TTLMS       int64  `json:"ttl_ms"`
}

type hostShareCreateRequest struct {
	DeviceID          string `json:"device_id"`
	RecipientUsername string `json:"recipient_username"`
	Write             bool   `json:"write"`
	TTLMS             int64  `json:"ttl_ms"`
}

func (s *Service) serveShareLinkCreate(w http.ResponseWriter, r *http.Request) {
	var request shareLinkCreateRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	link, token, err := s.sharing.CreateLink(r.Context(), identityFrom(r), request.DeviceID, request.SessionID, request.Write, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	view := newShareLinkView(link)
	writeFleetJSON(w, http.StatusOK, struct {
		shareLinkView
		Token string `json:"token"`
	}{shareLinkView: view, Token: token})
}

func (s *Service) serveShareLinkList(w http.ResponseWriter, r *http.Request) {
	links, err := s.sharing.ListLinks(r.Context(), identityFrom(r))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	views := make([]shareLinkView, 0, len(links))
	for _, link := range links {
		views = append(views, newShareLinkView(link))
	}
	writeFleetJSON(w, http.StatusOK, map[string][]shareLinkView{"links": views})
}

func (s *Service) serveShareLinkRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.sharing.RevokeLink(r.Context(), identityFrom(r), r.PathValue("id")); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// serveDeviceShareLinkCreate 处理 POST /share/device-links: 创建设备公开链接,
// 响应是唯一携带一次性 token 的入口 (列表/吊销响应不含 token)。
func (s *Service) serveDeviceShareLinkCreate(w http.ResponseWriter, r *http.Request) {
	var request deviceShareLinkCreateRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	link, token, err := s.sharing.CreateDeviceLink(r.Context(), identityFrom(r), request.DeviceID, request.Write, request.NotBeforeMS, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	view := newDeviceShareLinkView(link)
	writeFleetJSON(w, http.StatusOK, struct {
		deviceShareLinkView
		Token string `json:"token"`
	}{deviceShareLinkView: view, Token: token})
}

func (s *Service) serveDeviceShareLinkList(w http.ResponseWriter, r *http.Request) {
	links, err := s.sharing.ListDeviceLinks(r.Context(), identityFrom(r))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	views := make([]deviceShareLinkView, 0, len(links))
	for _, link := range links {
		views = append(views, newDeviceShareLinkView(link))
	}
	writeFleetJSON(w, http.StatusOK, map[string][]deviceShareLinkView{"links": views})
}

func (s *Service) serveDeviceShareLinkRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.sharing.RevokeDeviceLink(r.Context(), identityFrom(r), r.PathValue("id")); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) serveHostShareCreate(w http.ResponseWriter, r *http.Request) {
	var request hostShareCreateRequest
	if err := decodeFleetJSON(w, r, &request); err != nil {
		writeFleetFailure(w, err)
		return
	}
	// 设备授权与 deny 审计在 sharing.CreateHostShare 内先于用户名解析 (越权一律
	// 403+审计, 不泄露接收者是否存在); 接收者按用户名精确解析 (大小写不敏感),
	// 创建不要求前端持有用户目录 (/admin/users 仅超管), 普通设备 owner 与超管
	// 同一合同。两条错误路径都不接触任何密码或密钥材料。
	share, err := s.sharing.CreateHostShare(r.Context(), identityFrom(r), request.DeviceID, request.RecipientUsername, request.Write, time.Duration(request.TTLMS)*time.Millisecond)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, newHostShareView(share))
}

func (s *Service) serveHostShareList(w http.ResponseWriter, r *http.Request) {
	shares, err := s.sharing.ListHostShares(r.Context(), identityFrom(r))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	views := make([]hostShareView, 0, len(shares))
	for _, share := range shares {
		views = append(views, newHostShareView(share))
	}
	writeFleetJSON(w, http.StatusOK, map[string][]hostShareView{"shares": views})
}

func (s *Service) serveHostShareRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.sharing.RevokeHostShare(r.Context(), identityFrom(r), r.PathValue("id")); err != nil {
		writeFleetFailure(w, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// dialShareSupervisor 返回设备代管数据通路专用的 supervisor 客户端: 每次
// 拨号经 registry 请求一条出站桥接 (supervisor 协议字节透传), hello 摘要取
// 自设备控制通道上报的 state_digest。设备离线或未上报时拒绝。
func (s *Service) dialShareSupervisor(deviceID string) (*supervisor.Client, error) {
	digest, ok := s.registry.StateDigest(deviceID)
	if !ok {
		return nil, ipc.NewError(ipc.CodeDisconnected, "设备代理未上报状态摘要")
	}
	return supervisor.NewRemoteClient(func(ctx context.Context) (net.Conn, error) {
		return s.registry.RequestBridge(ctx, deviceID)
	}, digest), nil
}

type shareReadyFrame struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Permission string `json:"permission"`
	ExpiresAt  int64  `json:"expires_at"`
}

type shareErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// shareTerminalWS 串行化 viewer WS 的全部写 (二进制输出与文本控制帧共用
// 一条连接, coder/websocket 只允许一个并发写者); 读只有一个方向, 无需加锁。
type shareTerminalWS struct {
	conn *websocket.Conn

	writeMu      sync.Mutex
	writeTimeout time.Duration
}

func (v *shareTerminalWS) writeMessage(ctx context.Context, kind websocket.MessageType, payload []byte) error {
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	timeout := v.writeTimeout
	if timeout <= 0 {
		timeout = shareViewerWriteTimeout
	}
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return v.conn.Write(writeCtx, kind, payload)
}

func (v *shareTerminalWS) writeJSON(ctx context.Context, frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	v.writeMu.Lock()
	defer v.writeMu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return v.conn.Write(writeCtx, websocket.MessageText, payload)
}

type shareWSReader struct {
	conn *websocket.Conn
	buf  []byte
}

func (r *shareWSReader) Read(p []byte) (int, error) {
	for {
		if len(r.buf) > 0 {
			count := copy(p, r.buf)
			r.buf = r.buf[count:]
			return count, nil
		}
		kind, payload, err := r.conn.Read(context.Background())
		if err != nil {
			return 0, err
		}
		// viewer 输入只认二进制帧; 文本控制帧不进入终端字节流。
		if kind != websocket.MessageBinary {
			continue
		}
		r.buf = payload
	}
}

type shareWSWriter struct {
	viewer *shareTerminalWS
}

func (w shareWSWriter) Write(p []byte) (int, error) {
	if err := w.viewer.writeMessage(context.Background(), websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// discardInput 在输入方向被 Gate 拒绝后持续读并丢弃 viewer 消息: 只读或
// 权限收缩后会话保持 (输出继续), 控制帧 (ping/close) 也需要持续读取来应答。
// 读用 background ctx — coder/websocket 会在读 ctx 取消时直接拆连接, 那会
// 抢在终止错误帧写出之前把 viewer 连接杀掉。
func (v *shareTerminalWS) discardInput() error {
	for {
		if _, _, err := v.conn.Read(context.Background()); err != nil {
			return err
		}
	}
}

// shareResolveKey 是匿名公开链接 resolve 的限流键: 按客户端 IP 计数, 成功
// resolve 重置窗口; 随机 token 洪水刷不动 audit_log。
func (s *Service) shareResolveKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := "share-resolve|" + clientIP(r)
	if !s.shareResolveGate.Allow(key) {
		writeFleetJSON(w, http.StatusTooManyRequests, ipc.Failure(ipc.NewError(ipc.CodeForbidden, "尝试过于频繁，请稍后再试")))
		return "", false
	}
	return key, true
}

// recordShareResolveOutcome 只对 token 不存在计入限流退避 (随机 token 洪水);
// 吊销/过期/未生效/设备离线等真实 token 的拒绝不惩罚持有者。
func (s *Service) recordShareResolveOutcome(key string, err error) {
	if err == nil {
		s.shareResolveGate.RecordSuccess(key)
		return
	}
	if errors.Is(err, sharing.ErrTokenNotFound) {
		s.shareResolveGate.RecordFailure(key)
	}
}

// serveSharePublicTerminal 处理 GET /share/public/{token}: 浏览器普通导航
// (非 WS upgrade) 由配置的静态入口直接服务 SPA 页面, 不做 token 校验/审计/
// 持久化; WS upgrade 才是数据面 — 匿名 viewer 用公开 token 换取一次性授权
// (ResolveLink 已审计), 随后经出站桥接 attach 到分享会话, 双向字节流过
// sharing Gate: 输出始终放行, 输入仅 read_write, 吊销/过期/设备变化逐块
// 复查即时双向停止。
func (s *Service) serveSharePublicTerminal(w http.ResponseWriter, r *http.Request) {
	if !isShareWSUpgrade(r) {
		s.serveSharePublicPage(w, r)
		return
	}
	key, allowed := s.shareResolveKey(w, r)
	if !allowed {
		return
	}
	grant, err := s.sharing.ResolveLink(r.Context(), r.PathValue("token"))
	s.recordShareResolveOutcome(key, err)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	s.serveShareTerminal(w, r, grant, s.sharing.NewLinkGate(grant))
}

// serveSharePublicDeviceTerminal 处理 GET /share/public/device/{token}: 设备
// 公开链接的数据面, 与 serveSharePublicTerminal 同一合同 — 普通导航由静态
// 入口直接服务 SPA (不做 token 校验), WS upgrade 才进入 token 鉴权; 区别在
// 于 Grant 不带 SessionID, 数据面经 host agent 为访客新建终端 (而不是 attach
// 分享者的既有会话), 全程不接触主机密码或私钥。
func (s *Service) serveSharePublicDeviceTerminal(w http.ResponseWriter, r *http.Request) {
	if !isShareWSUpgrade(r) {
		s.serveSharePublicPage(w, r)
		return
	}
	key, allowed := s.shareResolveKey(w, r)
	if !allowed {
		return
	}
	grant, err := s.sharing.ResolveDeviceLink(r.Context(), r.PathValue("token"))
	s.recordShareResolveOutcome(key, err)
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	s.serveShareTerminal(w, r, grant, s.sharing.NewDeviceLinkGate(grant))
}

// isShareWSUpgrade 判定请求是否为 WebSocket 升级; 判定条件与
// websocket.Accept 的握手校验一致 (Connection/Upgrade 头 token, 大小写不敏感)。
func isShareWSUpgrade(r *http.Request) bool {
	return headerHasToken(r.Header, "Connection", "upgrade") && headerHasToken(r.Header, "Upgrade", "websocket")
}

func headerHasToken(header http.Header, key, token string) bool {
	for _, value := range header.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// serveSharePublicPage 用配置的静态入口服务 SPA (与 "/" 同一 index.html,
// 不重定向不另造 shell); token URL 一律 no-store。静态入口按 URL.Path 查
// 文件, 原始 token 路径可能撞上真实静态文件或被编码 dot-segment 校验拒绝,
// 因此克隆请求并把路径固定为 "/", 只保留 query 等上下文。
func (s *Service) serveSharePublicPage(w http.ResponseWriter, r *http.Request) {
	if s.sharePublicPage == nil {
		http.NotFound(w, r)
		return
	}
	page := r.Clone(r.Context())
	page.URL.Path = "/"
	page.URL.RawPath = ""
	page.RequestURI = "/"
	if page.URL.RawQuery != "" {
		page.RequestURI += "?" + page.URL.RawQuery
	}
	s.sharePublicPage.ServeHTTP(noStoreWriter{w}, page)
}

// noStoreWriter 在写出前强制 Cache-Control: no-store, 覆盖静态入口对
// text/html 默认的 no-cache — token URL 不允许任何缓存。
type noStoreWriter struct{ http.ResponseWriter }

func (w noStoreWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}

func (w noStoreWriter) Write(payload []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(payload)
}

// serveShareTerminalOpen 处理 GET /share/devices/{id}/terminal: 注册用户
// (AuthorizeTerminalOpen 已审计) 经 host agent 新建终端, 双向字节流过
// sharing Gate, 权限收缩即时生效。
func (s *Service) serveShareTerminalOpen(w http.ResponseWriter, r *http.Request) {
	grant, err := s.sharing.AuthorizeTerminalOpen(r.Context(), identityFrom(r), r.PathValue("id"))
	if err != nil {
		writeFleetFailure(w, err)
		return
	}
	s.serveShareTerminal(w, r, grant, s.sharing.NewHostGate(grant))
}

// serveShareTerminal 升级 viewer WS 并建立 gated 数据通路。grant 带
// SessionID 时 attach 既有会话 (公开链接), 否则经 host agent 新建终端
// (注册分享); 两条路径的泵与 Gate 语义完全一致。
func (s *Service) serveShareTerminal(w http.ResponseWriter, r *http.Request, grant *sharing.Grant, gate *sharing.Gate) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxShareViewerMessage)
	viewer := &shareTerminalWS{conn: conn}

	client, err := s.dialShareSupervisor(grant.DeviceID)
	if err != nil {
		s.failShareTerminal(viewer, err)
		return
	}
	sessionID, stream, err := s.openShareStream(r, client, grant)
	if err != nil {
		s.failShareTerminal(viewer, err)
		return
	}
	if err := viewer.writeJSON(r.Context(), shareReadyFrame{
		Type: "ready", SessionID: sessionID, Permission: string(grant.Permission), ExpiresAt: grant.ExpiresAt,
	}); err != nil {
		_ = stream.Close()
		_ = conn.Close(websocket.StatusInternalError, "ready failed")
		return
	}
	s.pumpShareTerminal(viewer, stream, gate)
}

func (s *Service) openShareStream(r *http.Request, client *supervisor.Client, grant *sharing.Grant) (string, *supervisor.Stream, error) {
	if grant.SessionID != "" {
		stream, err := client.Attach(r.Context(), grant.SessionID, nil)
		return grant.SessionID, stream, err
	}
	info, err := client.Create(r.Context(), supervisor.CreateOptions{})
	if err != nil {
		return "", nil, err
	}
	expect := &supervisor.Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation}
	stream, err := client.Attach(r.Context(), info.ID, expect)
	if err != nil {
		// attach 失败时回收刚创建的会话, 不在宿主上留下孤儿终端。
		killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Kill(killCtx, info.ID, expect)
		return "", nil, err
	}
	return info.ID, stream, nil
}

// pumpShareTerminal 双向泵送直到任一侧结束或授权失效: 输出 Gate.PipeOutput
// (终端到 viewer), 输入 Gate.PipeInput (viewer 到终端), 另有一个周期复查
// goroutine 兜住空闲连接 (无数据流时吊销/过期同样生效)。授权失效时任一
// goroutine 都会带着错误返回, 随后 best-effort 发错误帧并关闭; 阻塞的读
// 由 stream.Close 与 viewer 关闭唤醒。
func (s *Service) pumpShareTerminal(viewer *shareTerminalWS, stream *supervisor.Stream, gate *sharing.Gate) {
	pumpCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 3)
	go func() {
		done <- gate.PipeOutput(pumpCtx, shareWSWriter{viewer: viewer}, stream)
	}()
	go func() {
		err := gate.PipeInput(pumpCtx, stream, &shareWSReader{conn: viewer.conn})
		if errors.Is(err, sharing.ErrInputNotAllowed) {
			err = viewer.discardInput()
		}
		done <- err
	}()
	go func() {
		ticker := time.NewTicker(s.shareRevalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pumpCtx.Done():
				done <- pumpCtx.Err()
				return
			case <-ticker.C:
				if err := gate.Check(pumpCtx); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	err := <-done
	cancel()
	_ = stream.Close()
	code, message, ok := shareTermination(err)
	if !ok && err != nil {
		// 传输层错误 (桥接断开等) 先于授权复查到时不会映射终止码, 终止前
		// 强制重查一次授权: 过期/吊销仍下发可识别错误帧, 不让传输错误掩盖
		// 真实终止原因。
		checkCtx, checkCancel := context.WithTimeout(context.Background(), shareTerminateCheckTimeout)
		defer checkCancel()
		if checkErr := gate.CheckFresh(checkCtx); checkErr != nil {
			code, message, ok = shareTermination(checkErr)
		}
	}
	if ok {
		_ = viewer.writeJSON(context.Background(), shareErrorFrame{Type: "error", Code: code, Message: message})
	}
	status := websocket.StatusNormalClosure
	reason := "share ended"
	if err != nil {
		status = websocket.StatusPolicyViolation
		reason = "share terminated"
	}
	_ = viewer.conn.Close(status, reason)
	<-done
	<-done
}

func (s *Service) failShareTerminal(viewer *shareTerminalWS, err error) {
	code, message, ok := shareTermination(err)
	if !ok {
		code, message = "internal", "分享数据通路建立失败"
	}
	_ = viewer.writeJSON(context.Background(), shareErrorFrame{Type: "error", Code: code, Message: message})
	_ = viewer.conn.Close(websocket.StatusPolicyViolation, "share failed")
}

// shareTermination 把泵错误映射为可安全下发给 viewer 的终止码; 传输层
// 断开与内部错误不携带细节 (避免泄露数据库/内部状态)。
func shareTermination(err error) (string, string, bool) {
	switch {
	case err == nil:
		return "", "", false
	case errors.Is(err, sharing.ErrGrantExpired):
		return "expired", "分享授权已过期", true
	case errors.Is(err, sharing.ErrInputNotAllowed):
		return "read_only", "分享授权为只读", true
	case errors.Is(err, supervisor.ErrNotFound):
		return "not_found", "分享会话不存在", true
	case errors.Is(err, supervisor.ErrUnavailable):
		return "disconnected", "设备代理离线", true
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case ipc.CodeForbidden, ipc.CodeDisconnected, ipc.CodeNotFound:
			return string(appErr.Code), appErr.Message, true
		}
	}
	return "", "", false
}
