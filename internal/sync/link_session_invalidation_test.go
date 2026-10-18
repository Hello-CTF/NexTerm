package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestLinkSetChangedCredentialsInvalidateOldSessionOnFailedConnect(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-password")
	server.createUser(t, "bob", "bob-password")
	instance := newTestInstance(t, true)
	ctx := context.Background()
	link := func(username, password string) LinkResult {
		t.Helper()
		result, err := instance.service.LinkSet(ctx, LinkPatch{
			URL: testPtr(server.URL), Username: testPtr(username), Password: testPtr(password),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertNoOldIdentity := func() {
		t.Helper()
		status, err := instance.service.Status(ctx)
		if err != nil || status.LoggedIn || status.UserID != "" {
			t.Fatalf("old session remains visible: status=%+v err=%v", status, err)
		}
		view, err := instance.service.KindOptInGet(ctx)
		if err != nil || view.KnownHost || view.AIProfile || view.AIPermission || view.AIGrant || view.Preferences {
			t.Fatalf("old account opt-in remains readable: view=%+v err=%v", view, err)
		}
		if _, err := instance.service.KindOptInSet(ctx, KindOptInPatch{KnownHost: boolPtr(false)}); err == nil {
			t.Fatal("old account opt-in remains writable")
		} else {
			requireCode(t, err, ipc.CodeForbidden)
		}
		if _, found, err := instance.db.SettingGet(ctx, settingLinkSession); err != nil || found {
			t.Fatalf("old persisted session remains: found=%v err=%v", found, err)
		}
	}

	if result := link("alice", "alice-password"); result.Connect != LinkConnectConnected {
		t.Fatalf("initial link=%+v", result)
	}
	if result := link("bob", "wrong-password"); result.Connect != "" || result.LastError == "" {
		t.Fatalf("failed account switch not reported: %+v", result)
	}
	assertNoOldIdentity()

	if result := link("bob", "bob-password"); result.Connect != LinkConnectConnected {
		t.Fatalf("bob link=%+v", result)
	}
	oldPersisted, found := instance.service.engine.loadPersistedSession(ctx)
	if !found {
		t.Fatal("missing persisted session before password change")
	}
	if result := link("bob", "wrong-password"); result.Connect != "" || result.LastError == "" {
		t.Fatalf("failed password switch not reported: %+v", result)
	}
	assertNoOldIdentity()
	if _, err := instance.service.engine.restoreSession(ctx, RemoteConfig{
		URL: server.URL, Username: "bob", Password: "wrong-password",
	}, oldPersisted); !errors.Is(err, errSessionExpired) {
		t.Fatalf("old cookie restored under changed password: %v", err)
	}
}
