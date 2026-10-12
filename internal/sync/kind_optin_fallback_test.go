package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// 桌面匿名直连下 KindOptIn 回退引擎已登录会话 (preferenceUserID 同型):
// get/set 归属引擎会话用户, /rpc 注入身份优先于引擎会话, 引擎会话清空后回到匿名语义。
func TestKindOptInEngineSessionFallback(t *testing.T) {
	instance := newTestInstance(t, false)
	engine := instance.service.engine
	engine.sessionMu.Lock()
	engine.session = &remoteSession{key: "k", userID: "u-engine"}
	engine.sessionMu.Unlock()

	// 匿名 ctx 经引擎会话读到归属用户的默认开启状态
	view, err := instance.service.KindOptInGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.KnownHost || !view.AIProfile || !view.AIPermission || !view.Preferences {
		t.Fatalf("engine session user should see defaults on: %+v", view)
	}

	// 匿名 set 写入引擎会话用户
	view, err = instance.service.KindOptInSet(context.Background(), KindOptInPatch{KnownHost: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if view.KnownHost {
		t.Fatalf("set via engine session failed: %+v", view)
	}

	// 写入归属引擎会话用户, 不污染其他用户
	other, err := instance.service.KindOptInGet(WithUserID(context.Background(), "u-other"))
	if err != nil {
		t.Fatal(err)
	}
	if !other.KnownHost {
		t.Fatalf("u-other must keep defaults on: %+v", other)
	}

	// /rpc 注入身份优先于引擎会话
	ctxView, err := instance.service.KindOptInGet(WithUserID(context.Background(), "u-other"))
	if err != nil {
		t.Fatal(err)
	}
	if !ctxView.KnownHost {
		t.Fatalf("context identity must win over engine session: %+v", ctxView)
	}
	if _, err := instance.service.KindOptInSet(WithUserID(context.Background(), "u-ctx"), KindOptInPatch{AIProfile: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	engineView, err := instance.service.KindOptInGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !engineView.AIProfile || engineView.KnownHost {
		t.Fatalf("context identity write must not leak into engine session user: %+v", engineView)
	}

	// 引擎会话清空后回到匿名语义: get 全 false, set forbidden
	engine.sessionMu.Lock()
	engine.session = nil
	engine.sessionMu.Unlock()
	anon, err := instance.service.KindOptInGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if anon.KnownHost || anon.AIProfile || anon.AIPermission || anon.Preferences {
		t.Fatalf("anonymous get must be all false: %+v", anon)
	}
	if _, err := instance.service.KindOptInSet(context.Background(), KindOptInPatch{KnownHost: boolPtr(true)}); err == nil {
		t.Fatal("anonymous set must be forbidden")
	} else if appErr, ok := err.(*ipc.Error); !ok || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("set err = %v, want forbidden", err)
	}
}
