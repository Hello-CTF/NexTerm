package production

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// TestSyncCompareCommandRegistered 装配层接线: sync_compare 随 sync 模块注册到生产 dispatcher。
func TestSyncCompareCommandRegistered(t *testing.T) {
	production := newUpdateTestProduction(t, true, nil)
	for _, registered := range production.Dispatcher.Commands() {
		if registered == "sync_compare" {
			return
		}
	}
	t.Fatal("production is missing sync_compare")
}

// TestSyncCompareCommandWithoutLinkRejected 未配置同步链接时业务层如实拒绝(bad_param),
// 证明命令经 dispatcher 真实到达 sync.Service.Compare。
func TestSyncCompareCommandWithoutLinkRejected(t *testing.T) {
	production := newUpdateTestProduction(t, true, nil)
	response := dispatchHostKeyTest(t, production, "sync_compare", `null`)
	if response.OK {
		t.Fatal("compare without link must fail")
	}
	if response.Error == nil || response.Error.Code != ipc.CodeBadParam {
		t.Fatalf("error=%+v", response.Error)
	}
	if !strings.Contains(response.Error.Message, "同步链接") {
		t.Fatalf("message=%q", response.Error.Message)
	}
}

// TestSyncCompareCommandNonDesktopRejected 非桌面端(服务器模式)拒绝: 对比依赖桌面引擎会话。
func TestSyncCompareCommandNonDesktopRejected(t *testing.T) {
	ctx := t.Context()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	credentialVault := vault.Load(ctx, database)
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata("test", false))

	dispatcher := ipc.NewDispatcher()
	if err := registerSyncCompareCommands(dispatcher, syncService); err != nil {
		t.Fatal(err)
	}
	if err := registerSyncCompareCommands(dispatcher, syncService); err == nil {
		t.Fatal("duplicate sync_compare registration must fail")
	}
	response := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "sync_compare"}, ipc.Environment{})
	if response.OK {
		t.Fatal("non-desktop compare must fail")
	}
	if response.Error == nil || response.Error.Code != ipc.CodeUnsupported {
		t.Fatalf("error=%+v", response.Error)
	}
}
