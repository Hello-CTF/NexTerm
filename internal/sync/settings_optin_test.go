package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestKindOptInGetWithoutIdentityReturnsAllFalse(t *testing.T) {
	instance := newTestInstance(t, false)
	view, err := instance.service.KindOptInGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.KnownHost || view.AIProfile {
		t.Fatalf("anonymous get must be all false: %+v", view)
	}
}

func TestKindOptInSetWithoutIdentityForbidden(t *testing.T) {
	instance := newTestInstance(t, false)
	if _, err := instance.service.KindOptInSet(context.Background(), KindOptInPatch{KnownHost: boolPtr(true)}); err == nil {
		t.Fatal("anonymous set must be forbidden")
	} else if appErr, ok := err.(*ipc.Error); !ok || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("set err = %v", err)
	}
	// 匿名不得留下任何写入
	view, err := instance.service.KindOptInGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.KnownHost || view.AIProfile {
		t.Fatalf("anonymous set must not persist: %+v", view)
	}
}

func TestKindOptInSetAndGetPerUserIsolation(t *testing.T) {
	instance := newTestInstance(t, false)
	ctxA := WithUserID(context.Background(), "u-a")
	ctxB := WithUserID(context.Background(), "u-b")

	view, err := instance.service.KindOptInSet(ctxA, KindOptInPatch{KnownHost: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !view.KnownHost || !view.AIProfile {
		t.Fatalf("set view = %+v", view)
	}
	// A 的开关不影响 B 的默认开启状态
	viewB, err := instance.service.KindOptInGet(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if !viewB.KnownHost || !viewB.AIProfile {
		t.Fatalf("u-b 应保持默认开启: %+v", viewB)
	}
	// 底层键按用户隔离, 全局键不存在
	var globalRows int
	if err := instance.db.DB().QueryRowContext(context.Background(),
		"SELECT count(*) FROM setting WHERE key IN ('sync.optin.known_host', 'sync.optin.known_host.')").Scan(&globalRows); err != nil {
		t.Fatal(err)
	}
	if globalRows != 0 {
		t.Fatal("不得写全局 opt-in 键")
	}
	// 关闭 known_host 不影响 AI 档案的默认开启状态
	if _, err := instance.service.KindOptInSet(ctxA, KindOptInPatch{KnownHost: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	viewA, err := instance.service.KindOptInGet(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if viewA.KnownHost || !viewA.AIProfile {
		t.Fatalf("关闭 known_host 后状态错误: %+v", viewA)
	}
}

func TestKindOptInPatchOnlyTouchesSuppliedKinds(t *testing.T) {
	instance := newTestInstance(t, false)
	ctx := WithUserID(context.Background(), "u-a")
	if _, err := instance.service.KindOptInSet(ctx, KindOptInPatch{AIProfile: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	view, err := instance.service.KindOptInSet(ctx, KindOptInPatch{KnownHost: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !view.KnownHost || !view.AIProfile {
		t.Fatalf("view = %+v", view)
	}
}

func boolPtr(v bool) *bool { return &v }
