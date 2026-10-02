package session

import (
	"context"
	"testing"
	"time"
)

func TestDefaultReplayFitsPendingQueueBeforeReceiverBinds(t *testing.T) {
	manager, _, _, session := openTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	tab.terminal.Feed(make([]byte, manager.defaultReplay))
	attached := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1})
		attached <- err
	}()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("default replay blocked before a receiver could bind")
	}
	stats := manager.Hub().Stats()
	wantBytes := manager.defaultReplay + len(replayClear)
	if stats.QueuedBytes != wantBytes {
		t.Fatalf("pending replay bytes = %d, want %d", stats.QueuedBytes, wantBytes)
	}
	receiver := bindTestReceiver(t, manager, "b-1")
	assertFrameData(t, receiver, replayClear)
}
