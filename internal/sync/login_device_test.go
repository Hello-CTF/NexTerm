package sync

import (
	"context"
	"strings"
	"testing"
)

// A client that already logged in keeps presenting its login device token, so a username in backoff
// still admits it after the session is gone, while a client without the token is refused.
func TestLoginDeviceTokenSurvivesSessionLossAndRestart(t *testing.T) {
	fixture := newRemoteAPIFixture(t)
	fixture.createUser(t, "alice", "alice-pw-123")
	instance := newTestInstance(t, true)
	ctx := context.Background()
	config := RemoteConfig{URL: fixture.URL, Username: "alice", Password: "alice-pw-123"}
	engine := instance.service.engine

	if _, err := engine.Connect(ctx, config); err != nil {
		t.Fatal(err)
	}
	if got := engine.loadDeviceToken(ctx, config); got == "" {
		t.Fatal("login device token was not kept after login")
	}

	// Logout and session expiry clear the session but keep the device token.
	engine.resetSession()
	engine.clearPersistedSession(ctx)
	fixture.requireDevice.Store(true)

	if _, err := engine.Connect(ctx, config); err != nil {
		t.Fatalf("re-login with the stored device token failed: %v", err)
	}

	// The token is persisted: a fresh engine on the same store still presents it.
	restarted := New(instance.db, instance.vault, WithMetadata("test", true)).engine
	if _, err := restarted.Connect(ctx, config); err != nil {
		t.Fatalf("re-login after restart failed: %v", err)
	}

	// Another account on the same link slot must not receive alice's token.
	if got := engine.loadDeviceToken(ctx, RemoteConfig{URL: fixture.URL, Username: "bob"}); got != "" {
		t.Fatal("device token leaked to another username")
	}

	// Equivalent spellings of the link match; a different scheme or host does not.
	for _, same := range []RemoteConfig{
		{URL: fixture.URL + "/", Username: "alice"},
		{URL: fixture.URL, Username: "  Alice "},
	} {
		if engine.loadDeviceToken(ctx, same) == "" {
			t.Fatalf("device token not matched for %+v", same)
		}
	}
	for _, other := range []string{
		strings.Replace(fixture.URL, "http://", "https://", 1),
		strings.Replace(fixture.URL, "127.0.0.1", "localhost", 1),
	} {
		if engine.loadDeviceToken(ctx, RemoteConfig{URL: other, Username: "alice"}) != "" {
			t.Fatalf("device token matched a different endpoint %s", other)
		}
	}

	// Without the token the backed-off username refuses the login.
	engine.resetSession()
	engine.clearPersistedSession(ctx)
	engine.sessionMu.Lock()
	engine.device = nil
	engine.sessionMu.Unlock()
	if err := instance.db.SettingDelete(ctx, settingLinkDevice); err != nil {
		t.Fatal(err)
	}
	_, err := engine.Connect(ctx, config)
	if err == nil {
		t.Fatal("login without a device token must be refused while the username is in backoff")
	}
}
