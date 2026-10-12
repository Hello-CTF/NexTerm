package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

var (
	// ErrGuestControlOwner 拒绝访客写入: 标签页控制器被他人 (所有者或另一访客) 占有。
	ErrGuestControlOwner = errors.New("guest control: owner active")
	// ErrGuestControlBusy 拒绝访客写入: 另一访客的租约仍在有效期 (断线宽限窗内)。
	ErrGuestControlBusy = errors.New("guest control: another guest lease active")
)

// WriteGuest 以预览访客身份写入终端并按租约仲裁控制权: 判定与占领在 tab.mu 内
// 原子完成 (取锁顺序与 Write 一致: writeMu -> mu -> 解锁后 session.mu), 成功后
// 访客成为 controller 并持有 leaseTTL 的租约, 同一访客每次调用续租。所有者活跃
// (controller 被他人占有) 返回 ErrGuestControlOwner; 另一访客租约未过期返回
// ErrGuestControlBusy, 过期租约在判定时惰性清理。访客的 clientID 与其只读订阅
// 相同 ("preview-<shareID>"), 因此订阅断开时既有 orphan 清理会摘掉 controller,
// 租约则保留至到期, 同访客重连可立即恢复, 其他访客须等租约过期或被 ReleaseGuest
// 显式释放。KindRemote/非 ptyBacked 标签页不支持访客控制 (ErrUnsupported)。
func (m *Manager) WriteGuest(ctx context.Context, tabID, guest string, data []byte, leaseTTL time.Duration) error {
	m.notifyUserInput(tabID)
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.writeMu.Lock()
	defer tab.writeMu.Unlock()
	tab.mu.Lock()
	if tab.closed || tab.destroying {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if !ptyBacked(tab.session.asset.Kind) {
		tab.mu.Unlock()
		return ErrUnsupported
	}
	guestID := clientID(guest)
	if tab.guestLease != "" && tab.guestLease != guestID && !time.Now().Before(tab.guestLeaseUntil) {
		tab.clearGuestLeaseLocked()
	}
	if tab.controller != "" && tab.controller != guestID {
		tab.mu.Unlock()
		return ErrGuestControlOwner
	}
	if tab.guestLease != "" && tab.guestLease != guestID {
		tab.mu.Unlock()
		return ErrGuestControlBusy
	}
	takeOver := tab.controller != guestID
	var event ControlEvent
	if takeOver {
		if tab.grid != nil {
			if err := tab.grid.Claim(); err != nil {
				tab.mu.Unlock()
				return mapGridError(err)
			}
		}
		tab.controller = guestID
		tab.controlled = true
	}
	tab.guestLease = guestID
	tab.guestLeaseUntil = time.Now().Add(leaseTTL)
	channel := tab.channel
	generation := tab.generation
	exited := tab.exited
	if takeOver {
		event = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if takeOver {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	tab.session.mu.Lock()
	connected := tab.session.status == StatusConnected && tab.session.generation == generation
	tab.session.mu.Unlock()
	if !connected {
		return ErrDisconnected
	}
	if exited || channel == nil {
		return ErrTabClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAll(channel, data); err != nil {
		return err
	}
	m.transcriptInput(ctx, tab, data)
	return nil
}

// ReleaseGuest 释放访客的控制权与租约, grid 语义与 Release 一致 (交还观察者
// 模式) 并发控制事件; 幂等 — 访客不再占有控制权时调用为 no-op。租约随释放即
// 失效, 其他访客可立即接管。
func (m *Manager) ReleaseGuest(tabID, guest string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	guestID := clientID(guest)
	tab.mu.Lock()
	released := tab.controller == guestID
	if released && tab.grid != nil && !tab.hidden {
		if err := tab.grid.SetMode(terminalgrid.ModeObserver); err != nil {
			tab.mu.Unlock()
			return mapGridError(err)
		}
	}
	if released {
		tab.controller = ""
	}
	if tab.guestLease == guestID {
		tab.clearGuestLeaseLocked()
	}
	var event ControlEvent
	if released {
		event = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if released {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	return nil
}

func (t *Tab) clearGuestLeaseLocked() {
	t.guestLease = ""
	t.guestLeaseUntil = time.Time{}
}
