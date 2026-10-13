package sync

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// With a desktop anonymous direct connection, KindOptIn falls back to the engine's logged-in session (same shape as preferenceUserID):
// get/set belong to the engine session user, the /rpc injected identity takes precedence over the engine session, and clearing the engine session reverts to anonymous semantics.
func TestKindOptInEngineSessionFallback(t *testing.T) {
	instance := newTestInstance(t, false)
	engine := instance.service.engine
	engine.sessionMu.Lock()
	engine.session = &remoteSession{key: "k", userID: "u-engine"}
	engine.sessionMu.Unlock()

	// The anonymous ctx reads the owning user's default-on state via the engine session.
	view, err := instance.service.KindOptInGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.KnownHost || !view.AIProfile || !view.AIPermission || !view.Preferences {
		t.Fatalf("engine session user should see defaults on: %+v", view)
	}

	// Anonymous set writes the engine session user.
	view, err = instance.service.KindOptInSet(context.Background(), KindOptInPatch{KnownHost: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if view.KnownHost {
		t.Fatalf("set via engine session failed: %+v", view)
	}

	// The write goes to the engine session user without polluting other users.
	other, err := instance.service.KindOptInGet(WithUserID(context.Background(), "u-other"))
	if err != nil {
		t.Fatal(err)
	}
	if !other.KnownHost {
		t.Fatalf("u-other must keep defaults on: %+v", other)
	}

	// The /rpc injected identity takes precedence over the engine session.
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

	// With the engine session cleared, anonymous semantics return: get is all false, set is forbidden.
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
