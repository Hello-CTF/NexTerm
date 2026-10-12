package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/shellintegr"
)

// KindRemote 标记远程会话壳: 无本地进程、无 transport, 输出由推流方 (桌面端)
// 经 FeedRemote 推入, 只读订阅 (Spectator/预览) 与真实终端标签页同路径取流。
const KindRemote = "remote"

// MaxRemoteSessionsPerUser 是单账号同时存活的远程会话壳上限, 防止壳只建不收
// 耗尽内存; 显式结束 (CloseRemote) 或空闲回收后释放名额。
const MaxRemoteSessionsPerUser = 32

var (
	ErrRemoteLimit        = errors.New("remote session limit reached")
	ErrRemoteFeedAttached = errors.New("remote session already has an active feed")
)

// RemoteSessionOptions 是注册远程会话壳的入参: OwnerID 即归属账号 (归属校验与
// 预览创建共用 TabOwner); Title/AssetID 只是元数据 (展示用), 不参与 byAsset
// 去重 — 壳不经过 Connect, asset.ID 永不注册进 m.byAsset, 避免与真实资产冲突。
type RemoteSessionOptions struct {
	OwnerID string
	Title   string
	AssetID string
	Cols    uint32
	Rows    uint32
}

// RegisterRemoteSession 在会话 hub 里注册一个无本地进程的终端标签页: 内存终端
// 模型即输出缓冲与快照源, 之后 FeedRemote 推入的输出既更新屏幕 (Spectator 首帧
// 快照) 又按既有 feed 路径广播给订阅者。壳的会话 status 恒为 connected, 生命周期
// 由 EndRemote/CloseRemote 或空闲回收终结。
func (m *Manager) RegisterRemoteSession(ctx context.Context, options RemoteSessionOptions) (TabInfo, error) {
	if options.OwnerID == "" {
		return TabInfo{}, errors.New("remote session owner is empty")
	}
	cols, rows := options.Cols, options.Rows
	if cols == 0 && rows == 0 {
		cols, rows = 80, 24
	}
	if !validSize(cols, rows) {
		return TabInfo{}, ErrInvalidSize
	}
	if err := ctx.Err(); err != nil {
		return TabInfo{}, err
	}

	now := time.Now()
	sessionCtx, sessionCancel := context.WithCancel(m.ctx)
	session := &Session{
		ID:          newID(),
		CreatedAt:   now,
		asset:       Asset{ID: options.AssetID, Name: options.Title, Kind: KindRemote},
		userID:      options.OwnerID,
		status:      StatusConnected,
		generation:  1,
		ctx:         sessionCtx,
		cancel:      sessionCancel,
		tabs:        make(map[string]*Tab),
		connectDone: make(chan struct{}),
	}
	if session.asset.ID == "" {
		session.asset.ID = "remote/" + session.ID
	}
	session.finishConnectLocked(nil)
	session.remoteActivity = now

	tabID := newID()
	terminal, err := m.terminals.NewTerminal(TerminalConfig{TabID: tabID, SessionID: session.ID, Cols: cols, Rows: rows})
	if err != nil {
		sessionCancel()
		return TabInfo{}, err
	}

	tabCtx, tabCancel := context.WithCancel(m.ctx)
	tab := &Tab{
		ID: tabID, SessionID: session.ID, session: session, terminal: terminal,
		cols: cols, rows: rows, encoding: canonicalEncoding(""),
		generation: 1, subscribers: make(map[string]subscriber),
		ctx: tabCtx, cancel: tabCancel, responses: newResponseQueue(1), feedGate: make(chan struct{}, 1),
		cwdTracker: shellintegr.NewTracker(), commandTrack: shellintegr.NewCommandTracker(),
	}
	tab.feedGate <- struct{}{}
	// 壳没有 transport, 终端模型对查询序列 (如 DA) 的应答无处可去, 直接丢弃;
	// 推流方的本地终端已在自己一侧应答过。
	terminal.SetResponseHandler(func([]byte) {})

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		tabCancel()
		sessionCancel()
		terminal.Close()
		return TabInfo{}, ErrSessionClosed
	}
	owned := 0
	for _, existing := range m.sessions {
		if existing.asset.Kind == KindRemote && existing.userID == options.OwnerID {
			owned++
		}
	}
	if owned >= MaxRemoteSessionsPerUser {
		m.mu.Unlock()
		tabCancel()
		sessionCancel()
		terminal.Close()
		return TabInfo{}, ErrRemoteLimit
	}
	m.sessions[session.ID] = session
	m.tabs[tab.ID] = tab
	session.tabs[tab.ID] = tab
	session.mu.Lock()
	connectedEvent := session.statusEventLocked(StatusConnected, nil)
	session.mu.Unlock()
	tab.mu.Lock()
	controlEvent := tab.controlEventLocked()
	info := tab.infoLocked()
	tab.mu.Unlock()
	m.mu.Unlock()

	m.emit(ctx, TopicSessionStatus, connectedEvent)
	m.emit(ctx, TopicTerminalControl, controlEvent)
	m.transcriptStarted(ctx, session)
	return info, nil
}

// remoteTab 取出标签页并校验它是存活的远程壳; 之后各操作共用这一入口,
// 保证普通终端标签页不会被远程通道误写。
func (m *Manager) remoteTab(tabID string) (*Tab, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return nil, err
	}
	tab.mu.Lock()
	defer tab.mu.Unlock()
	if tab.session.asset.Kind != KindRemote {
		return nil, ErrUnsupported
	}
	if tab.closed || tab.destroying {
		return nil, ErrTabClosed
	}
	return tab, nil
}

func (s *Session) touchRemoteActivity() {
	s.mu.Lock()
	s.remoteActivity = time.Now()
	s.mu.Unlock()
}

// remoteWatchingLocked 报告壳是否有订阅观看者 (Spectator/预览 viewer);
// 调用方须持有 session.mu, 取 tab.mu 的顺序与 detachChannelsLocked 一致。
func (s *Session) remoteWatchingLocked() bool {
	for _, tab := range s.tabs {
		tab.mu.Lock()
		watching := len(tab.subscribers) > 0
		tab.mu.Unlock()
		if watching {
			return true
		}
	}
	return false
}

// RemoteSessionInfo 返回远程壳的标签页信息; 非远程壳返回 ErrUnsupported。
func (m *Manager) RemoteSessionInfo(tabID string) (TabInfo, error) {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	return tab.Info(), nil
}

// FeedRemote 把推流方的一帧远程输出推入壳: 更新屏幕模型并广播给全部订阅者
// (Spectator 收到的是与真实终端一致的二进制输出帧)。end 之后拒绝再推。
func (m *Manager) FeedRemote(ctx context.Context, tabID string, data []byte) error {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	exited := tab.exited
	generation := tab.generation
	tab.mu.Unlock()
	if exited {
		return ErrTabClosed
	}
	if err := m.feed(ctx, tab, generation, data); err != nil {
		return err
	}
	tab.session.touchRemoteActivity()
	return nil
}

// EndRemote 标记远程壳结束 (推流方终端已退出): 与真实进程退出同语义 — 标签页
// 保留、快照仍可被新订阅者读取, 只是不再接受新输出。重复调用是幂等 no-op。
func (m *Manager) EndRemote(tabID string, exitCode *int) error {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	if tab.exited {
		tab.mu.Unlock()
		return nil
	}
	tab.exited = true
	exitEvent := tab.exitEventLocked(exitCode)
	controlEvent := tab.controlEventLocked()
	tab.mu.Unlock()
	tab.session.touchRemoteActivity()
	m.emit(context.Background(), TopicTerminalExit, exitEvent)
	m.emit(context.Background(), TopicTerminalControl, controlEvent)
	return nil
}

// ResizeRemote 调整远程壳的屏幕尺寸 (推流方终端 resize 时同步), 快照随之反映
// 新尺寸; 与交互式 Resize 不同, 壳没有 grid 协商, 直接改模型。
func (m *Manager) ResizeRemote(ctx context.Context, tabID string, cols, rows uint32) error {
	if !validSize(cols, rows) {
		return ErrInvalidSize
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	if err := tab.terminal.Resize(int(cols), int(rows)); err != nil {
		return err
	}
	tab.mu.Lock()
	tab.cols = cols
	tab.rows = rows
	controlEvent := tab.controlEventLocked()
	tab.mu.Unlock()
	tab.session.touchRemoteActivity()
	m.emit(ctx, TopicTerminalControl, controlEvent)
	m.transcriptResize(ctx, tab, cols, rows)
	return nil
}

// AttachRemoteFeed 占用壳的推流口: 同一壳同一时间只允许一条推流连接, 防止两条
// 流交错写花屏幕; 占用期间空闲回收挂起 (连接本身即活动)。返回壳所属会话供
// DetachRemoteFeed 成对释放。
func (m *Manager) AttachRemoteFeed(tabID string) (*Session, error) {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return nil, err
	}
	session := tab.session
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil, ErrSessionClosed
	}
	tab.mu.Lock()
	exited := tab.exited
	tab.mu.Unlock()
	if exited {
		return nil, ErrTabClosed
	}
	if session.remoteFeeds > 0 {
		return nil, ErrRemoteFeedAttached
	}
	session.remoteFeeds++
	session.remoteActivity = time.Now()
	return session, nil
}

// DetachRemoteFeed 释放推流口并把空闲计时重置到断连时刻: 断线后壳保留,
// 可重连续推, 直到空闲超时回收。
func (m *Manager) DetachRemoteFeed(session *Session) {
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.remoteFeeds > 0 {
		session.remoteFeeds--
	}
	session.remoteActivity = time.Now()
	session.mu.Unlock()
}

// CloseRemote 显式终结远程壳: 整个会话连同标签页从 hub 移除, 订阅通道随之
// 关闭 (只读预览 viewer 收到 ended)。与 EndRemote 不同, 快照不再保留。
func (m *Manager) CloseRemote(tabID string) error {
	tab, err := m.remoteTab(tabID)
	if err != nil {
		return err
	}
	return m.reap(tab.SessionID)
}

// reapRemote 是空闲回收对远程壳的收割路径: 与 reap 同语义, 但候选条件是
// "无推流连接且距上次活动已超时", 并以候选时刻的活动时间戳复查防误收。
func (m *Manager) reapRemote(id string, activity time.Time) error {
	m.mu.Lock()
	session := m.sessions[id]
	if session == nil {
		m.mu.Unlock()
		return nil
	}
	session.mu.Lock()
	if session.asset.Kind != KindRemote || session.closed || session.remoteFeeds != 0 || session.remoteWatchingLocked() || !session.remoteActivity.Equal(activity) {
		session.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	session.closed = true
	session.status = StatusDisconnected
	session.generation++
	cancel := session.cancel
	session.finishConnectLocked(ErrSessionClosed)
	session.finishReconnectLocked(ErrSessionClosed)
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
		delete(m.tabs, tab.ID)
		delete(m.throttleVersions, tab.ID)
		delete(m.throttleChannels, tab.ID)
	}
	session.tabs = make(map[string]*Tab)
	delete(m.sessions, id)
	if m.byAsset[session.asset.ID] == session.ID {
		delete(m.byAsset, session.asset.ID)
	}
	disconnectedEvent := session.statusEventLocked(StatusDisconnected, nil)
	session.mu.Unlock()
	m.mu.Unlock()

	cancel()
	for _, tab := range tabs {
		m.closeTabResources(tab)
	}
	m.emit(context.Background(), TopicSessionStatus, disconnectedEvent)
	m.transcriptEnded(id)
	return nil
}
