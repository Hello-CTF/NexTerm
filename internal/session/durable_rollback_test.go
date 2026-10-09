package session

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestDurableCreateOpenRollbackDoesNotKillReplacement(t *testing.T) {
	provider := newFakeDurableProvider()
	const tabID = "create-rollback-aba"
	emitter, attached, unblock := blockFirstRollbackControl(tabID)
	defer unblock()
	manager, connected := newDurableRollbackManager(t, provider, emitter, "create-rollback-asset")
	firstResult := make(chan error, 1)
	go func() {
		_, err := manager.OpenTab(context.Background(), OpenTabOptions{
			TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "create-a", Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{},
		})
		firstResult <- err
	}()
	waitRollbackSignal(t, attached)
	first, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan error, 1)
	go func() { closed <- manager.CloseTab(tabID) }()
	waitFor(t, func() bool {
		_, err := manager.Tab(tabID)
		return errors.Is(err, ErrTabNotFound) && first.Info().Exited && fakeDurableRecordKills(provider, tabID) == 1
	})

	secondInfo, err := manager.OpenTab(context.Background(), OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-b", ChannelID: "create-b", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Tab(secondInfo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("replacement reused the original tab instance")
	}
	unblock()
	if err := <-firstResult; !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("original OpenTab error = %v, want ErrSessionClosed", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	registered, err := manager.Tab(tabID)
	if err != nil || registered != second {
		t.Fatalf("original rollback replaced B: tab=%p want=%p err=%v", registered, second, err)
	}
	if kills := fakeDurableRecordKills(provider, tabID); kills != 0 {
		t.Fatalf("original create rollback killed B %d times", kills)
	}
	firstAttachment := first.durable.(*fakeDurableAttachment)
	secondAttachment := second.durable.(*fakeDurableAttachment)
	if got := firstAttachment.closeCount.Load(); got != 1 {
		t.Fatalf("A attachment close count = %d, want one", got)
	}
	if got := secondAttachment.closeCount.Load(); got != 0 {
		t.Fatalf("B attachment was detached %d times", got)
	}
	if got := first.terminal.(*fakeTerminal).closes(); got != 1 {
		t.Fatalf("A emulator close count = %d, want one", got)
	}
	if got := second.terminal.(*fakeTerminal).closes(); got != 0 {
		t.Fatalf("B emulator was closed %d times", got)
	}
	if err := secondAttachment.emit([]byte("b-alive")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return bytes.Contains(second.terminal.(*fakeTerminal).bytes(), []byte("b-alive")) })
}

func TestDurableRecoveryOpenRollbackDoesNotDetachReplacement(t *testing.T) {
	provider := newFakeDurableProvider()
	const tabID = "recovery-rollback-aba"
	seed, err := provider.Create(context.Background(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	emitter, attached, unblock := blockFirstRollbackControl(tabID)
	defer unblock()
	manager, connected := newDurableRollbackManager(t, provider, emitter, "recovery-rollback-asset")
	firstResult := make(chan error, 1)
	go func() {
		_, err := manager.OpenTab(context.Background(), OpenTabOptions{
			TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "recovery-a", Cols: 80, Rows: 24,
			Durable: &DurableTabOptions{Recover: true},
		})
		firstResult <- err
	}()
	waitRollbackSignal(t, attached)
	first, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}

	disconnected := make(chan error, 1)
	go func() { disconnected <- manager.Disconnect(connected.ID) }()
	waitFor(t, func() bool {
		_, tabErr := manager.Tab(tabID)
		_, sessionErr := manager.Session(connected.ID)
		return errors.Is(tabErr, ErrTabNotFound) && errors.Is(sessionErr, ErrSessionNotFound) && first.Info().Exited
	})
	reconnected, err := manager.Connect(context.Background(), connected.Asset())
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := manager.OpenTab(context.Background(), OpenTabOptions{
		TabID: tabID, SessionID: reconnected.ID, ClientID: "client-b", ChannelID: "recovery-b", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Tab(secondInfo.ID)
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-firstResult; !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("original recovery OpenTab error = %v, want ErrSessionClosed", err)
	}
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
	registered, err := manager.Tab(tabID)
	if err != nil || registered != second {
		t.Fatalf("original recovery rollback replaced B: tab=%p want=%p err=%v", registered, second, err)
	}
	if kills := fakeDurableRecordKills(provider, tabID); kills != 0 {
		t.Fatalf("recovery rollback killed %d durable processes", kills)
	}
	firstAttachment := first.durable.(*fakeDurableAttachment)
	secondAttachment := second.durable.(*fakeDurableAttachment)
	if got := firstAttachment.closeCount.Load(); got != 1 {
		t.Fatalf("recovered A attachment close count = %d, want one", got)
	}
	if got := secondAttachment.closeCount.Load(); got != 0 {
		t.Fatalf("recovered B attachment was detached %d times", got)
	}
	if got := first.terminal.(*fakeTerminal).closes(); got != 1 {
		t.Fatalf("recovered A emulator close count = %d, want one", got)
	}
	if got := second.terminal.(*fakeTerminal).closes(); got != 0 {
		t.Fatalf("recovered B emulator was closed %d times", got)
	}
	if create, attach, _ := provider.counts(); create != 1 || attach != 2 {
		t.Fatalf("recovery reran command or attached incorrectly: create=%d attach=%d", create, attach)
	}
	if err := secondAttachment.emit([]byte("b-recovery-alive")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return bytes.Contains(second.terminal.(*fakeTerminal).bytes(), []byte("b-recovery-alive"))
	})
}

func newDurableRollbackManager(t *testing.T, provider base.DurableProvider, emitter Emitter, assetID string) (*Manager, *Session) {
	t.Helper()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider, Emitter: emitter,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: assetID, Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connected
}

func blockFirstRollbackControl(tabID string) (Emitter, <-chan struct{}, func()) {
	attached := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	blocked := false
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic != TopicTerminalControl {
			return nil
		}
		control := event.Payload.(ControlEvent)
		mu.Lock()
		shouldBlock := control.TabID == tabID && !blocked
		if shouldBlock {
			blocked = true
		}
		mu.Unlock()
		if shouldBlock {
			close(attached)
			<-release
		}
		return nil
	})
	return emitter, attached, unblock
}

func waitRollbackSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("initial attach did not reach the rollback checkpoint")
	}
}

func fakeDurableRecordKills(provider *fakeDurableProvider, id string) int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	record := provider.records[id]
	if record == nil {
		return 0
	}
	return record.killed
}
