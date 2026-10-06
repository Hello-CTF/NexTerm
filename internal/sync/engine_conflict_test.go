package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

// 真实 HTTP: 过期 head 的推送必须映射为 errHeadMismatch, 而不是普通 IPC 错误。
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
	if err := client.login(ctx, config.Username, config.Password); err != nil {
		t.Fatal(err)
	}
	userID, err := client.me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.push(ctx, genesisHead(userID), []WireObject{{ID: ids.New(), Blob: []byte("v1")}}); err != nil {
		t.Fatal(err)
	}
	// 再用过期 head 推送: 必须得到 errHeadMismatch 而非普通 IPC 错误。
	err = func() error {
		_, err := client.push(ctx, genesisHead(userID), []WireObject{{ID: ids.New(), Blob: []byte("v2")}})
		return err
	}()
	if !errors.Is(err, errHeadMismatch) {
		t.Fatalf("stale head push err=%v, want errHeadMismatch", err)
	}
}

// 引擎在真实 409 后必须重拉并冲突重试, 最终收敛。
func TestEngineRetriesOnReal409Conflict(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	deviceA := newTestDevice(t)
	deviceB := newTestDevice(t)

	putDeviceGroup(t, deviceA, ids.New(), nil, "A 分组", 100)
	syncDevice(t, deviceA, server, "alice", "alice-pw-123")

	// B 首次推送被服务端以 409 拒绝一次; 引擎必须重试成功并计入冲突。
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
