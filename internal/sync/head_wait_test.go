package sync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func syncedWaitClient(t *testing.T, server *testSyncServer, device *testDevice) (*remoteClient, string) {
	t.Helper()
	syncDevice(t, device, server, "alice", "alice-pw-123")
	client, head, ok, err := device.engine.remoteWaitTarget(context.Background())
	if err != nil || !ok || head == "" {
		t.Fatalf("wait target ok=%v head=%q err=%v", ok, head, err)
	}
	return client, head
}

func TestHeadWaitAnswersAtOnceWhenTheHeadAlreadyMoved(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	client, _ := syncedWaitClient(t, server, newTestDevice(t))
	start := time.Now()
	response, err := client.wait(context.Background(), "stale-head")
	if err != nil || !response.Changed || response.Head == "" {
		t.Fatalf("wait = %+v, %v; want an immediate change", response, err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("a moved head waited %v", time.Since(start))
	}
}

func TestHeadWaitWakesOnAnotherDevicesPush(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)
	client, head := syncedWaitClient(t, server, deviceA)
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")

	result := make(chan *WaitResponse, 1)
	go func() {
		response, err := client.wait(context.Background(), head)
		if err != nil {
			t.Error(err)
		}
		result <- response
	}()
	time.Sleep(100 * time.Millisecond)
	putDeviceSnippet(t, deviceB, ids.New(), "from-b", "uptime", 500)
	pushed := time.Now()
	syncDevice(t, deviceB, server, "alice", "alice-pw-123")
	select {
	case response := <-result:
		if response == nil || !response.Changed || response.Head == head {
			t.Fatalf("wait after a push = %+v, want the new head", response)
		}
		if waited := time.Since(pushed); waited > headWaitRecheck {
			t.Fatalf("push woke the wait after %v, want well under the %v recheck", waited, headWaitRecheck)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a push from another device did not wake the wait")
	}
}

func TestHeadWaitTimesOutQuietly(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	client, head := syncedWaitClient(t, server, newTestDevice(t))
	var response WaitResponse
	status, _, err := client.post(context.Background(), "/sync/v2/wait", WaitRequest{Protocol: ProtocolVersion, KnownHead: head, TimeoutMS: 1}, &response, false)
	if err != nil || status != http.StatusOK || response.Changed || response.Head != head {
		t.Fatalf("quiet wait status=%d response=%+v err=%v", status, response, err)
	}
}
