package session

import (
	"bytes"
	"context"
	"testing"
)

// ws-reconnect-replay 的服务端半边：浏览器 WS 掉线 → hub 摘除订阅 →
// 同一通道 id 重连（bind）→ 重新 attach → scrollback 重放到新接收方。
// 这条时序是真实浏览器验收锁定的行为：任何一环回归（摘除不干净、重放丢失、
// 订阅没重建、重连后实时输出断流）都会让真实检查退化为超时。
func TestChannelDropRebindReattachReplaysScrollback(t *testing.T) {
	manager, connector, terminals, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	receiver := bindTestReceiver(t, manager, "a-1")

	before := []byte("nx-marker-before-drop")
	if err := channel.emit(before); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, receiver, before)

	// WS 掉线：receiver 关闭同步触发 OnChannelClose → DetachChannel。
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Subscribers; got != 0 {
		t.Fatalf("subscribers after drop = %d, want 0", got)
	}
	if got := tab.Info().Controller; got != "" {
		t.Fatalf("controller after drop = %q, want released", got)
	}

	// 掉线期间的输出只进 scrollback，没有订阅者可投。
	during := []byte("nx-output-during-drop")
	if err := channel.emit(during); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(terminals.terminal(tab.ID).bytes(), during) })

	// 浏览器用同一通道 id 重连（先 bind），再由 reopen 回调重新 attach。
	rebound := bindTestReceiver(t, manager, "a-1")
	info, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1", ReplayBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if info.Subscribers != 1 || info.Controller != "client-a" {
		t.Fatalf("reattach info = %+v", info)
	}
	assertFrameData(t, rebound, replayClear)
	replay := receiveTestFrame(t, rebound)
	if !bytes.Contains(replay.Data, before) || !bytes.Contains(replay.Data, during) {
		t.Fatalf("replay lost scrollback: %q", replay.Data)
	}

	// 重连后的实时输出继续走新接收方。
	after := []byte("nx-live-after-reattach")
	if err := channel.emit(after); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, rebound, after)
}

// 半开重叠：服务端还没发现旧连接死掉，浏览器已经用同一通道 id 重连（新 bind）。
// 旧接收方迟到的 Close 被 hub 的 generation 守卫判为 stale，不得摘掉新绑定 ——
// 否则真实网络抖动下的重连会被自己误杀，订阅计数和重放一起丢。
func TestChannelRebindBeforeOldReceiverCloseKeepsNewBinding(t *testing.T) {
	manager, connector, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	old := bindTestReceiver(t, manager, "a-1")

	before := []byte("nx-marker-half-open")
	if err := channel.emit(before); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, old, before)

	rebound := bindTestReceiver(t, manager, "a-1")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if got := tab.Info().Subscribers; got != 1 {
		t.Fatalf("subscribers after stale close = %d, want 1", got)
	}

	info, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-1", ReplayBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if info.Subscribers != 1 || info.Controller != "client-a" {
		t.Fatalf("reattach info = %+v", info)
	}
	assertFrameData(t, rebound, replayClear)
	replay := receiveTestFrame(t, rebound)
	if !bytes.Contains(replay.Data, before) {
		t.Fatalf("replay lost scrollback: %q", replay.Data)
	}

	after := []byte("nx-live-after-half-open")
	if err := channel.emit(after); err != nil {
		t.Fatal(err)
	}
	assertFrameData(t, rebound, after)
}
