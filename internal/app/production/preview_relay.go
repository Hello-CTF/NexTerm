package production

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/coder/websocket"
)

// 桌面端只读预览中继 (第 3 期 b): 把本地终端标签页的输出经服务端远程会话壳
// 中继成匿名可开的只读预览链接。preview_share_create 建壳 + 建链接 + 起中继
// goroutine: AttachSpectator 订阅本地 tab (订阅首帧是本地屏幕快照, 跳过不转 —
// 壳会给自己的观众产快照, 中继只推增量二进制输出帧), 输出原样写 feed WS;
// tab 退出/关闭/账号切换时发 end 控制帧并 DELETE 壳。feed 断线按退避重拨续推
// (壳在服务端保留), 重拨预算耗尽即收尾。单 tab 同时只允许一条活跃中继, 重复
// 创建直接返回内存中持有的既有链接 (token 不持久化, 仅中继存活期间可查)。

const (
	// previewRelayPollInterval 是空闲心跳间隔: spectator 通道不带控制事件,
	// tab 退出/resize/账号切换都在心跳里探测 (pumpSharePreview 同款模式)。
	previewRelayPollInterval = time.Second
	// previewRelayRedialBudget 是 feed 断线后的重拨总预算: 预算内靠 hub 订阅
	// 队列 (512 帧/16MiB) 吸收输出不丢帧; 耗尽即收尾, 避免背压长期阻塞本地终端。
	previewRelayRedialBudget = 45 * time.Second
	previewRelayRedialMin    = time.Second
	previewRelayRedialMax    = 15 * time.Second
	previewRelayWriteTimeout = 10 * time.Second
	previewRelayEndTimeout   = 3 * time.Second
	previewRelayStopTimeout  = 10 * time.Second
)

// previewRelayEndFrame 是推流方主动收尾的控制帧: 服务端按进程退出语义关闭壳的
// 写入方向 (快照保留); 随后的 DELETE 才把壳连同预览流一并终结。
var previewRelayEndFrame = []byte(`{"type":"end"}`)

// previewRelayRemote 是中继对远程代理面的最小依赖; *syncservice.RemoteProxy 实现。
type previewRelayRemote interface {
	RemoteSessionCreate(ctx context.Context, title, assetID string, cols, rows uint32) (*syncservice.RemoteSession, error)
	CreatePreviewLink(ctx context.Context, sessionID string, ttlMS int64) (*syncservice.RemotePreviewLinkCreate, error)
	RemoteSessionDelete(ctx context.Context, id string) (syncservice.RemoteOK, error)
	RemoteSessionFeedDial(ctx context.Context, sessionID string) (*websocket.Conn, error)
	ServerBaseURL(ctx context.Context) (string, error)
}

// previewRelaySyncStatus 是中继对账号状态的探测面; *syncservice.Service 实现。
type previewRelaySyncStatus interface {
	Status(ctx context.Context) (syncservice.Status, error)
}

// previewRelayFeed 是远程推流连接的最小写面; *websocket.Conn 直接实现, 测试
// 用脚本化假连接注入写失败以驱动重拨路径。
type previewRelayFeed interface {
	Write(ctx context.Context, messageType websocket.MessageType, data []byte) error
	Close(code websocket.StatusCode, reason string) error
	CloseNow() error
}

type previewRelayFeedDial func(ctx context.Context, shellID string) (previewRelayFeed, error)

type previewRelay struct {
	tabID     string
	channelID string
	shellID   string
	token     string
	url       string
	username  string
	base      string
	cols      uint32
	rows      uint32
	ctx       context.Context
	cancel    context.CancelFunc
	receiver  *hub.Receiver
	// feed 是 run goroutine 持有的当前活跃推流连接 (重拨后更新), 仅 run 读写,
	// finish 由 run 的 defer 调用, 同 goroutine 免锁。
	feed previewRelayFeed
	once sync.Once
}

func (r *previewRelay) result() *previewShareCreateResult {
	return &previewShareCreateResult{Token: r.token, URL: r.url, SessionID: r.shellID}
}

type previewRelayManager struct {
	sessions   *session.Manager
	remote     previewRelayRemote
	syncStatus previewRelaySyncStatus
	dialFeed   previewRelayFeedDial
	logger     *slog.Logger

	pollInterval time.Duration
	redialBudget time.Duration

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	relays map[string]*previewRelay
	closed bool
	wg     sync.WaitGroup
}

func newPreviewRelayManager(sessions *session.Manager, remote previewRelayRemote, syncStatus previewRelaySyncStatus, logger *slog.Logger) *previewRelayManager {
	if logger == nil {
		logger = slog.Default()
	}
	manager := &previewRelayManager{
		sessions: sessions, remote: remote, syncStatus: syncStatus, logger: logger,
		pollInterval: previewRelayPollInterval, redialBudget: previewRelayRedialBudget,
		relays: make(map[string]*previewRelay),
	}
	if remote != nil {
		manager.dialFeed = func(ctx context.Context, shellID string) (previewRelayFeed, error) {
			conn, err := remote.RemoteSessionFeedDial(ctx, shellID)
			if err != nil {
				return nil, err
			}
			return conn, nil
		}
	}
	return manager
}

func (m *previewRelayManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	return nil
}

// Create 为本地 tab 建只读预览: 注册远程壳 → 建预览链接 → 拨 feed → 订阅本地
// 输出起中继。任一步失败按序回收 (壳必删, 预览链接随 TTL 自然过期); tab 已有
// 活跃中继时直接返回既有链接。
func (m *previewRelayManager) Create(ctx context.Context, tabID string, ttlMS int64) (*previewShareCreateResult, error) {
	if m.sessions == nil || m.remote == nil || m.dialFeed == nil {
		return nil, ipc.NewError(ipc.CodeUnsupported, "只读预览仅在桌面端可用")
	}
	tab, err := m.sessions.Tab(tabID)
	if err != nil {
		return nil, ipc.NewError(ipc.CodeNotFound, "终端标签页不存在或已关闭")
	}
	info := tab.Info()
	if info.Exited {
		return nil, ipc.NewError(ipc.CodeBadParam, "终端已退出，无法创建只读预览")
	}

	m.mu.Lock()
	if existing := m.relays[tabID]; existing != nil {
		result := existing.result()
		m.mu.Unlock()
		return result, nil
	}
	if m.closed || m.ctx == nil {
		m.mu.Unlock()
		return nil, ipc.NewError(ipc.CodeDisconnected, "预览中继服务未就绪")
	}
	m.mu.Unlock()

	// 标题/资产元数据从 tab 会话信息取 (远程壳仅作展示元数据, 不参与资产去重)。
	title, assetID := "", ""
	if owner, err := m.sessions.Session(tab.SessionID); err == nil {
		asset := owner.Asset()
		title, assetID = asset.Name, asset.ID
	}

	shell, err := m.remote.RemoteSessionCreate(ctx, title, assetID, info.Cols, info.Rows)
	if err != nil {
		return nil, err
	}
	cleanupShell := true
	defer func() {
		if cleanupShell {
			deleteCtx, cancel := context.WithTimeout(context.Background(), previewRelayStopTimeout)
			defer cancel()
			if _, err := m.remote.RemoteSessionDelete(deleteCtx, shell.SessionID); err != nil {
				m.logger.Warn("preview relay: cleanup remote session failed", "shell", shell.SessionID, "error", err)
			}
		}
	}()

	link, err := m.remote.CreatePreviewLink(ctx, shell.SessionID, ttlMS)
	if err != nil {
		return nil, err
	}
	base, err := m.remote.ServerBaseURL(ctx)
	if err != nil {
		return nil, err
	}
	feed, err := m.dialFeed(ctx, shell.SessionID)
	if err != nil {
		return nil, err
	}

	username := ""
	if m.syncStatus != nil {
		if status, err := m.syncStatus.Status(ctx); err == nil {
			username = status.Username
		}
	}

	relayCtx, cancel := context.WithCancel(m.ctx)
	relay := &previewRelay{
		tabID: tabID, channelID: ids.New(), shellID: shell.SessionID,
		token: link.Token, url: base + "/share/preview/" + link.Token,
		username: username, base: base,
		cols: info.Cols, rows: info.Rows,
		ctx: relayCtx, cancel: cancel,
	}
	// 订阅本地输出: 首帧快照不转发 (壳会给自己的观众产快照), 只推增量二进制帧。
	if _, err := m.sessions.AttachSpectator(relayCtx, tabID, session.SpectateOptions{
		ChannelID: relay.channelID, ClientID: "preview-relay",
	}); err != nil {
		cancel()
		_ = feed.CloseNow()
		return nil, err
	}
	receiver, err := m.sessions.Bind(relay.channelID)
	if err != nil {
		cancel()
		_ = feed.CloseNow()
		_ = m.sessions.DetachChannel(relay.channelID)
		return nil, err
	}
	relay.receiver = receiver

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		_ = receiver.Close()
		_ = feed.CloseNow()
		_ = m.sessions.DetachChannel(relay.channelID)
		return nil, ipc.NewError(ipc.CodeDisconnected, "预览中继服务未就绪")
	}
	if existing := m.relays[tabID]; existing != nil {
		// 并发创建竞态: 后到者让位, 自建的壳由 defer 回收。
		result := existing.result()
		m.mu.Unlock()
		cancel()
		_ = receiver.Close()
		_ = feed.CloseNow()
		_ = m.sessions.DetachChannel(relay.channelID)
		return result, nil
	}
	m.relays[tabID] = relay
	m.wg.Add(1)
	m.mu.Unlock()
	cleanupShell = false
	go relay.run(m, feed)
	return relay.result(), nil
}

// Active 返回 tab 当前活跃中继的远程壳 id (桌面菜单按它过滤「进行中的预览」);
// 无活跃中继返回空。
func (m *previewRelayManager) Active(tabID string) previewShareActiveResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if relay := m.relays[tabID]; relay != nil {
		return previewShareActiveResult{SessionID: relay.shellID}
	}
	return previewShareActiveResult{}
}

func (m *previewRelayManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run 是中继 goroutine 本体: 泵输出直到 tab 结束/关闭、账号切换、服务停止或
// feed 不可恢复; 退出必经 finish 收尾 (end 帧 + 删壳 + 退订)。
func (r *previewRelay) run(m *previewRelayManager, feed previewRelayFeed) {
	defer m.wg.Done()
	defer r.cancel()
	r.feed = feed
	defer func() { m.finish(r, r.feed) }()
	r.pump(m, feed)
}

func (r *previewRelay) pump(m *previewRelayManager, feed previewRelayFeed) {
	cols, rows := r.cols, r.rows
	var pending *hub.Frame
	for {
		if r.ctx.Err() != nil {
			return
		}
		if pending == nil {
			frameCtx, cancel := context.WithTimeout(r.ctx, m.pollInterval)
			frame, err := r.receiver.Next(frameCtx)
			cancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					if !m.pollRelay(r, feed, &cols, &rows) {
						return
					}
					continue
				}
				// 通道关闭 = tab 关闭/manager 停止/退订, 收尾。
				return
			}
			if frame.Kind != hub.FrameBinary {
				// 快照等 JSON 帧不转发: 壳的观众从壳的终端模型取快照。
				_ = r.receiver.Ack(frame.Sequence)
				continue
			}
			pending = &frame
		}
		writeCtx, cancel := context.WithTimeout(r.ctx, previewRelayWriteTimeout)
		err := feed.Write(writeCtx, websocket.MessageBinary, pending.Data)
		cancel()
		if err == nil {
			_ = r.receiver.Ack(pending.Sequence)
			pending = nil
			continue
		}
		_ = feed.CloseNow()
		feed = r.redial(m, cols, rows)
		r.feed = feed
		if feed == nil {
			return
		}
	}
}

// redial 在预算内按退避重拨 feed (壳在服务端保留可续推); 重拨成功补发当前尺寸
// (断线期间的 resize 控制帧可能已丢), 预算耗尽或服务停止返回 nil。
func (r *previewRelay) redial(m *previewRelayManager, cols, rows uint32) previewRelayFeed {
	backoff := previewRelayRedialMin
	deadline := time.Now().Add(m.redialBudget)
	for {
		feed, err := m.dialFeed(r.ctx, r.shellID)
		if err == nil {
			m.writeResize(r, feed, cols, rows)
			return feed
		}
		if r.ctx.Err() != nil || time.Now().Add(backoff).After(deadline) {
			return nil
		}
		select {
		case <-r.ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > previewRelayRedialMax {
			backoff = previewRelayRedialMax
		}
	}
}

// pollRelay 空闲心跳: tab 消失/退出或账号切换 → false (收尾); resize → 补发
// 控制帧 (best effort, 写失败由输出路径的重拨兜底)。
func (m *previewRelayManager) pollRelay(r *previewRelay, feed previewRelayFeed, cols, rows *uint32) bool {
	tab, err := m.sessions.Tab(r.tabID)
	if err != nil {
		return false
	}
	info := tab.Info()
	if info.Exited {
		return false
	}
	if info.Cols != *cols || info.Rows != *rows {
		*cols, *rows = info.Cols, info.Rows
		m.writeResize(r, feed, info.Cols, info.Rows)
	}
	return m.accountIntact(r)
}

// accountIntact 探测账号切换/断开: 链接被清除、用户名或服务端地址变化 → 中继
// 失去归属, 停止; 状态读取失败 (vault 锁定等瞬态) 不动中继, 401 重登由
// remoteCall 兜底, 因此 LoggedIn 抖动不在此判定。
func (m *previewRelayManager) accountIntact(r *previewRelay) bool {
	if m.syncStatus != nil {
		status, err := m.syncStatus.Status(r.ctx)
		if err == nil && (!status.Configured || (r.username != "" && status.Username != r.username)) {
			return false
		}
	}
	base, err := m.remote.ServerBaseURL(r.ctx)
	if err == nil && base != r.base {
		return false
	}
	return true
}

func (m *previewRelayManager) writeResize(r *previewRelay, feed previewRelayFeed, cols, rows uint32) {
	payload, err := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(r.ctx, previewRelayWriteTimeout)
	defer cancel()
	_ = feed.Write(writeCtx, websocket.MessageText, payload)
}

// finish 统一收尾 (幂等): 摘 map → 退订本地输出 → end 帧 → 关 feed → DELETE 壳。
// 摘 map 先于网络清理, 保证收尾期间的新创建拿到全新中继; end/删除用独立超时
// 上下文 (中继 ctx 已取消), 失败只记日志, 壳由服务端空闲回收兜底。
func (m *previewRelayManager) finish(r *previewRelay, feed previewRelayFeed) {
	r.once.Do(func() {
		m.mu.Lock()
		if m.relays[r.tabID] == r {
			delete(m.relays, r.tabID)
		}
		m.mu.Unlock()
		if r.receiver != nil {
			_ = r.receiver.Close()
		}
		if err := m.sessions.DetachChannel(r.channelID); err != nil {
			m.logger.Debug("preview relay: detach channel failed", "tab", r.tabID, "error", err)
		}
		if feed != nil {
			endCtx, cancel := context.WithTimeout(context.Background(), previewRelayEndTimeout)
			_ = feed.Write(endCtx, websocket.MessageText, previewRelayEndFrame)
			cancel()
			_ = feed.Close(websocket.StatusNormalClosure, "preview relay stopped")
		}
		deleteCtx, cancel := context.WithTimeout(context.Background(), previewRelayStopTimeout)
		defer cancel()
		if _, err := m.remote.RemoteSessionDelete(deleteCtx, r.shellID); err != nil {
			m.logger.Warn("preview relay: delete remote session failed", "shell", r.shellID, "error", err)
		}
	})
}

var _ Component = (*previewRelayManager)(nil)

type previewShareCreateRequest struct {
	TabID string `json:"tabId"`
	TTLMS int64  `json:"ttlMs"`
}

// previewShareCreateResult 的 token 只在创建当场随响应下发用于拼/展示公开
// URL, 不持久化; sessionId 是远程壳 id, 桌面菜单按它过滤「进行中的预览」。
type previewShareCreateResult struct {
	Token     string `json:"token"`
	URL       string `json:"url"`
	SessionID string `json:"sessionId"`
}

type previewShareActiveRequest struct {
	TabID string `json:"tabId"`
}

type previewShareActiveResult struct {
	SessionID string `json:"sessionId,omitempty"`
}

func registerPreviewRelayCommands(dispatcher *ipc.Dispatcher, relay *previewRelayManager) error {
	registrations := []func() error{
		func() error {
			return ipc.RegisterNested(dispatcher, "preview_share_create", func(ctx context.Context, _ *ipc.Call, input previewShareCreateRequest) (*previewShareCreateResult, error) {
				if input.TabID == "" || input.TTLMS <= 0 {
					return nil, ipc.NewError(ipc.CodeBadParam, "tabId/ttlMs 不能为空")
				}
				return relay.Create(ctx, input.TabID, input.TTLMS)
			})
		},
		func() error {
			return ipc.RegisterNested(dispatcher, "preview_share_active", func(ctx context.Context, _ *ipc.Call, input previewShareActiveRequest) (previewShareActiveResult, error) {
				if input.TabID == "" {
					return previewShareActiveResult{}, ipc.NewError(ipc.CodeBadParam, "tabId 不能为空")
				}
				return relay.Active(input.TabID), nil
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
