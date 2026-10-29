package session

import (
	"errors"
	"sync/atomic"
	"testing"
)

// After the helper confirmed the kill, a failing channel close must not leave
// the unregistered tab half torn down: nothing can retry its close once the
// manager forgot it, so its context, pump and subscribers would leak.
func TestDurableCloseReleasesTheTabWhenTheChannelCloseFailsAfterTheKill(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "close-fails", "a-1", false)
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider.attachment(info.ID, 0).fakeChannel.closeHook = func() error {
		if calls.Add(1) == 1 {
			return nil // the kill closes the attachment
		}
		return errors.New("close failed after kill")
	}
	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
	if _, err := manager.Tab(info.ID); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("tab still registered: %v", err)
	}
	select {
	case <-tab.ctx.Done():
	default:
		t.Fatal("the removed tab was never torn down: its context is still live")
	}
}
