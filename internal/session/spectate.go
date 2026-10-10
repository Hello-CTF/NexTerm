package session

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// SpectateSnapshotType 是只读预览通道首帧的 type 值, 前端据此渲染当前屏幕。
const SpectateSnapshotType = "snapshot"

// spectateSnapshot 是只读预览通道的首帧 (JSON 文本帧): 当前屏幕的纯文本行
// 与光标位置, 不含滚动历史与字符属性; 之后通道上只有实时输出 (二进制帧)。
type spectateSnapshot struct {
	Type      string   `json:"type"`
	Lines     []string `json:"lines"`
	CursorRow int      `json:"cursor_row"`
	CursorCol int      `json:"cursor_col"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
}

type SpectateOptions struct {
	ClientID  string
	ChannelID string
}

// AttachSpectator 以只读方式订阅标签页输出: feed 锁内先把当前屏幕快照写进
// 通道再注册订阅, 快照与后续实时输出之间无缺口无重复。观看者不取得控制权,
// 不参与 grid 协商, 输入路径对它的通道不存在; 离线/断开走既有
// DetachChannel 清理。快照不含字符属性 (服务端屏幕模型只存文本)。
func (m *Manager) AttachSpectator(ctx context.Context, tabID string, options SpectateOptions) (TabInfo, error) {
	if options.ChannelID == "" {
		return TabInfo{}, hub.ErrInvalidChannel
	}
	tab, err := m.Tab(tabID)
	if err != nil {
		return TabInfo{}, err
	}
	attachCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(tab.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	if err := tab.lockFeed(attachCtx); err != nil {
		return TabInfo{}, err
	}
	defer tab.unlockFeed()

	m.mu.Lock()
	if m.closed || m.tabs[tab.ID] != tab {
		m.mu.Unlock()
		return TabInfo{}, ErrTabClosed
	}
	if owner := m.channelTabs[options.ChannelID]; owner != "" && owner != tab.ID {
		m.mu.Unlock()
		return TabInfo{}, errors.New("channel already attached to another tab")
	}
	m.channelTabs[options.ChannelID] = tab.ID
	m.mu.Unlock()

	tab.mu.Lock()
	old, replaced := tab.subscribers[options.ChannelID]
	if replaced {
		delete(tab.subscribers, options.ChannelID)
		tab.releaseOrphanControllerLocked()
	}
	tab.mu.Unlock()
	producer, err := m.bus.Producer(options.ChannelID)
	if err != nil {
		m.releaseChannelReservation(tab.ID, options.ChannelID)
		return TabInfo{}, err
	}
	if replaced {
		_ = old.producer.Close()
	}
	attached := false
	defer func() {
		if !attached {
			_ = producer.Close()
			m.releaseChannelReservation(tab.ID, options.ChannelID)
		}
	}()
	if err := m.bus.DiscardPending(options.ChannelID); err != nil {
		return TabInfo{}, err
	}

	frame := spectateSnapshot{Type: SpectateSnapshotType}
	if core, ok := tab.terminal.(interface{ Snapshot() terminal.Snapshot }); ok {
		snapshot := core.Snapshot()
		frame.Lines = snapshot.Lines
		frame.CursorRow = snapshot.CursorRow
		frame.CursorCol = snapshot.CursorCol
		frame.Cols = snapshot.Cols
		frame.Rows = snapshot.Rows
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return TabInfo{}, err
	}
	if err := producer.SendJSON(attachCtx, payload); err != nil {
		return TabInfo{}, err
	}

	m.mu.Lock()
	if m.closed || m.tabs[tab.ID] != tab || m.channelTabs[options.ChannelID] != tab.ID {
		m.mu.Unlock()
		return TabInfo{}, hub.ErrDetached
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		m.mu.Unlock()
		return TabInfo{}, ErrTabClosed
	}
	tab.subscribers[options.ChannelID] = subscriber{client: clientID(options.ClientID), producer: producer}
	info := tab.infoLocked()
	event := tab.controlEventLocked()
	tab.mu.Unlock()
	m.mu.Unlock()
	attached = true
	m.emit(attachCtx, TopicTerminalControl, event)
	return info, nil
}

// TabOwner 返回打开该标签页所属会话的账号用户 ID; 桌面本地或后台会话为 ""。
// tab.session 在 OpenTab 创建时写入后不再变化, 与 runPump 一样免 tab.mu 读取,
// 避免与既有 session.mu -> tab.mu 的取锁顺序倒置。
func (m *Manager) TabOwner(tabID string) (string, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return "", err
	}
	session := tab.session
	if session == nil {
		return "", ErrTabNotFound
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.userID, nil
}
