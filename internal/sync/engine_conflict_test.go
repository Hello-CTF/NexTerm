package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// Real HTTP: pushing with a stale head must map to errHeadMismatch, not a generic IPC error.
func TestRemoteClientPushStaleHeadGetsConflict(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	device := newTestDevice(t)
	ctx := context.Background()

	config := device.config(server, "alice", "alice-pw-123")
	client, err := newRemoteClient(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.login(ctx, config.Username, config.Password); err != nil {
		t.Fatal(err)
	}
	userID, err := client.me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.push(ctx, genesisHead(userID), []WireObject{{ID: ids.New(), Blob: []byte("v1")}}); err != nil {
		t.Fatal(err)
	}
	// Pushing again with a stale head: it must yield errHeadMismatch, not a generic IPC error.
	err = func() error {
		_, err := client.push(ctx, genesisHead(userID), []WireObject{{ID: ids.New(), Blob: []byte("v2")}})
		return err
	}()
	if !errors.Is(err, errHeadMismatch) {
		t.Fatalf("stale head push err=%v, want errHeadMismatch", err)
	}
}

// After a real 409 the engine must re-pull and retry the conflict, converging in the end.
func TestEngineRetriesOnReal409Conflict(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	putDeviceGroup(t, deviceA, ids.New(), nil, "A 分组", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	// B's first push is rejected by the server with 409 once; the engine must retry successfully and count the conflict.
	server.failNextPushWith409 = true
	putDeviceGroup(t, deviceB, ids.New(), nil, "B 分组", 100)
	report, err := deviceB.engine.Sync(context.Background(), deviceB.config(server, "alice", "alice-pw-123"))
	if err != nil {
		t.Fatalf("sync after 409: %v (warnings=%v)", err, report.Warnings)
	}
	if report.Conflicts != 1 {
		t.Fatalf("expected one conflict retry, report=%+v", report)
	}
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")
	ctx := context.Background()
	groups, err := deviceA.db.GroupList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, group := range groups {
		names[group.Name] = true
	}
	if !names["A 分组"] || !names["B 分组"] {
		t.Fatalf("deviceA groups=%v, want both", names)
	}
}
