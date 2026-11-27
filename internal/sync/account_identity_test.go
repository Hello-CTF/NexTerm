package sync

import (
	"context"
	"encoding/json"
	"testing"
)

// After a restart the session is restored only by a later round; until then the linked account's preferences
// stay readable and a change is written to that account, and a different link no longer reaches them.
func TestPreferencesUseLinkedAccountBeforeSessionRestore(t *testing.T) {
	ctx := context.Background()
	instance := newTestInstance(t, true)
	server := newTestSyncServer(t)
	const password = "alice-password"
	server.createUser(t, "alice", password)
	if _, err := instance.service.LinkSet(ctx, LinkPatch{
		URL: testPtr(server.URL), Username: testPtr("alice"), Password: testPtr(password),
	}); err != nil {
		t.Fatal(err)
	}
	const key = "terminal.keywordHighlights"
	enabled := json.RawMessage(`{"presets":["errors","warnings"],"rules":[]}`)
	if _, err := instance.service.PreferenceUpdate(ctx, PreferenceUpdate{Set: map[string]json.RawMessage{key: enabled}}); err != nil {
		t.Fatal(err)
	}

	instance.service.engine.sessionMu.Lock()
	instance.service.engine.session = nil
	instance.service.engine.sessionMu.Unlock()

	view, err := instance.service.PreferenceView(ctx)
	if err != nil {
		t.Fatalf("linked account preferences unavailable before session restore: %v", err)
	}
	if string(view.Overrides[key]) != string(enabled) {
		t.Fatalf("override = %s, want %s", view.Overrides[key], enabled)
	}
	all := json.RawMessage(`{"presets":["errors","warnings","success"],"rules":[]}`)
	if _, err := instance.service.PreferenceUpdate(ctx, PreferenceUpdate{Set: map[string]json.RawMessage{key: all}}); err != nil {
		t.Fatalf("write before session restore: %v", err)
	}
	collected, err := instance.service.engine.collectPreferenceKeys(ctx, instance.service, key)
	if err != nil {
		t.Fatal(err)
	}
	if collected != string(all) {
		t.Fatalf("the change is not in the account's synced preferences: %s", collected)
	}

	server.createUser(t, "bob", "bob-password")
	if _, err := instance.service.LinkSet(ctx, LinkPatch{Username: testPtr("bob"), Password: testPtr("wrong")}); err != nil {
		t.Logf("link change: %v", err)
	}
	instance.service.engine.sessionMu.Lock()
	instance.service.engine.session = nil
	instance.service.engine.sessionMu.Unlock()
	if _, err := instance.service.PreferenceView(ctx); err == nil {
		t.Fatal("a different link must not read the previous account's preferences")
	}
}

func (e *Engine) collectPreferenceKeys(ctx context.Context, s *Service, key string) (string, error) {
	userID, err := s.preferenceUserID(ctx)
	if err != nil {
		return "", err
	}
	objects := map[string]localObject{}
	if err := e.collectPreferences(ctx, userID, objects); err != nil {
		return "", err
	}
	object, ok := objects[preferenceObjectPrefix+key]
	if !ok {
		return "", nil
	}
	var payload preferenceObject
	if err := json.Unmarshal(object.plaintext, &payload); err != nil {
		return "", err
	}
	return string(payload.Value), nil
}
