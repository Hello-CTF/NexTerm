package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type preferenceFixture struct {
	*accountFixture
	database *store.Store
}

func newPreferenceFixture(t *testing.T, auth string) *preferenceFixture {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	config := testConfig(t, false)
	config.Options.Auth = auth
	accounts := account.New(database.DB())
	config.Accounts = accounts
	config.Settings = database
	server, httpServer := newTestHTTP(t, config)
	return &preferenceFixture{
		accountFixture: &accountFixture{
			server: server, http: httpServer, accounts: accounts, db: database.DB(),
			client: &http.Client{},
		},
		database: database,
	}
}

func (f *preferenceFixture) createUser(t *testing.T, username, password string) *accountTestSession {
	t.Helper()
	if _, err := f.accounts.CreateUser(context.Background(), username, "", password); err != nil {
		t.Fatal(err)
	}
	return f.login(t, username, password)
}

func preferenceMap(t *testing.T, call accountCall, field string) map[string]any {
	t.Helper()
	value, ok := call.body[field].(map[string]any)
	if !ok {
		t.Fatalf("response %s missing: %v", field, call.body)
	}
	return value
}

func TestPreferenceHTTPUserOverrideLifecycle(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOn)
	fixture.initSuperadmin(t, "root", "root-password1")
	user := fixture.createUser(t, "alice", "alice-password1")

	call := fixture.call(t, http.MethodGet, "/auth/preferences", nil, user, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("get status=%d body=%v", call.status, call.body)
	}
	for _, field := range []string{"defaults", "overrides", "effective"} {
		if values := preferenceMap(t, call, field); len(values) != 0 {
			t.Fatalf("initial %s=%v", field, values)
		}
	}

	call = fixture.call(t, http.MethodPut, "/auth/preferences", map[string]any{
		"set": map[string]any{"appearance.terminalTheme": "light", "keybinding.closeTab": nil},
	}, user, user.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("put status=%d body=%v", call.status, call.body)
	}
	overrides := preferenceMap(t, call, "overrides")
	if overrides["appearance.terminalTheme"] != "light" {
		t.Fatalf("overrides=%v", overrides)
	}
	if value, ok := overrides["keybinding.closeTab"]; !ok || value != nil {
		t.Fatalf("explicit null override missing: %v", overrides)
	}
	effective := preferenceMap(t, call, "effective")
	if effective["appearance.terminalTheme"] != "light" {
		t.Fatalf("effective=%v", effective)
	}

	call = fixture.call(t, http.MethodPut, "/auth/preferences", map[string]any{
		"clear": []string{"appearance.terminalTheme"},
	}, user, user.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("clear status=%d body=%v", call.status, call.body)
	}
	overrides = preferenceMap(t, call, "overrides")
	if _, ok := overrides["appearance.terminalTheme"]; ok {
		t.Fatalf("override must be cleared: %v", overrides)
	}
	if value, ok := overrides["keybinding.closeTab"]; !ok || value != nil {
		t.Fatalf("unrelated override must survive: %v", overrides)
	}
}

func TestPreferenceHTTPDefaultOverrideMerge(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOn)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password1")
	user := fixture.createUser(t, "bob", "bob-password1")

	call := fixture.call(t, http.MethodPut, "/admin/preferences", map[string]any{
		"set": map[string]any{
			"appearance.terminalTheme": "dark",
			"appearance.uiFontScale":   1.25,
			"keybinding.closeTab":      "Mod+Shift+w",
		},
	}, admin, admin.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("admin put status=%d body=%v", call.status, call.body)
	}
	defaults := preferenceMap(t, call, "defaults")
	if defaults["appearance.terminalTheme"] != "dark" || defaults["appearance.uiFontScale"] != 1.25 {
		t.Fatalf("defaults=%v", defaults)
	}

	call = fixture.call(t, http.MethodGet, "/auth/preferences", nil, user, "", nil)
	if call.status != http.StatusOK {
		t.Fatalf("user get status=%d body=%v", call.status, call.body)
	}
	if overrides := preferenceMap(t, call, "overrides"); len(overrides) != 0 {
		t.Fatalf("user overrides=%v", overrides)
	}
	effective := preferenceMap(t, call, "effective")
	if effective["appearance.terminalTheme"] != "dark" || effective["appearance.uiFontScale"] != 1.25 || effective["keybinding.closeTab"] != "Mod+Shift+w" {
		t.Fatalf("inherited effective=%v", effective)
	}

	call = fixture.call(t, http.MethodPut, "/auth/preferences", map[string]any{
		"set": map[string]any{"appearance.terminalTheme": "interface", "keybinding.closeTab": nil},
	}, user, user.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("override put status=%d body=%v", call.status, call.body)
	}
	effective = preferenceMap(t, call, "effective")
	if effective["appearance.terminalTheme"] != "interface" || effective["appearance.uiFontScale"] != 1.25 {
		t.Fatalf("merged effective=%v", effective)
	}
	if value, ok := effective["keybinding.closeTab"]; !ok || value != nil {
		t.Fatalf("unbound override must win over default: %v", effective)
	}

	call = fixture.call(t, http.MethodPut, "/auth/preferences", map[string]any{
		"clear": []string{"appearance.terminalTheme", "keybinding.closeTab"},
	}, user, user.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("clear status=%d body=%v", call.status, call.body)
	}
	effective = preferenceMap(t, call, "effective")
	if effective["appearance.terminalTheme"] != "dark" || effective["keybinding.closeTab"] != "Mod+Shift+w" {
		t.Fatalf("reset-to-default effective=%v", effective)
	}
}

func TestPreferenceHTTPRoleBoundaries(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOn)
	admin, _ := fixture.initSuperadmin(t, "root", "root-password1")
	user := fixture.createUser(t, "carol", "carol-password1")

	if call := fixture.call(t, http.MethodGet, "/admin/preferences", nil, user, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("user admin get status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPut, "/admin/preferences", map[string]any{
		"set": map[string]any{"appearance.terminalTheme": "dark"},
	}, user, user.csrf, nil); call.status != http.StatusForbidden {
		t.Fatalf("user admin put status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodGet, "/admin/preferences", nil, admin, "", nil); call.status != http.StatusOK {
		t.Fatalf("admin get status=%d", call.status)
	}

	if call := fixture.call(t, http.MethodGet, "/auth/preferences", nil, nil, "", nil); call.status != http.StatusUnauthorized {
		t.Fatalf("anonymous get status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPut, "/auth/preferences", map[string]any{
		"set": map[string]any{"appearance.terminalTheme": "light"},
	}, user, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", call.status)
	}
	if call := fixture.call(t, http.MethodPut, "/admin/preferences", map[string]any{
		"set": map[string]any{"appearance.terminalTheme": "dark"},
	}, admin, "", nil); call.status != http.StatusForbidden {
		t.Fatalf("admin missing csrf status=%d", call.status)
	}

	call := fixture.call(t, http.MethodPut, "/auth/preferences", map[string]any{
		"set": map[string]any{"appearance.terminalTheme": "light"},
	}, admin, admin.csrf, nil)
	if call.status != http.StatusOK {
		t.Fatalf("superadmin own override status=%d body=%v", call.status, call.body)
	}
	if overrides := preferenceMap(t, call, "overrides"); overrides["appearance.terminalTheme"] != "light" {
		t.Fatalf("superadmin overrides=%v", overrides)
	}
}

func TestPreferenceHTTPAuthOffClosesRoutes(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOff)

	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/auth/preferences"},
		{http.MethodPut, "/auth/preferences"},
		{http.MethodGet, "/admin/preferences"},
		{http.MethodPut, "/admin/preferences"},
	} {
		call := fixture.call(t, test.method, test.path, map[string]any{}, nil, "", nil)
		if call.status != http.StatusForbidden {
			t.Fatalf("auth=off %s %s status=%d", test.method, test.path, call.status)
		}
	}
}

func TestPreferenceHTTPValidation(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOn)
	fixture.initSuperadmin(t, "root", "root-password1")
	user := fixture.createUser(t, "dave", "dave-password1")

	rejected := []map[string]any{
		{"set": map[string]any{"dek_envelope": "secret"}},
		{"set": map[string]any{"password": "hunter22"}},
		{"set": map[string]any{"ai.models": "[]"}},
		{"set": map[string]any{"arbitrary.key": "value"}},
		{"set": map[string]any{"appearance.terminalTheme": "blue"}},
		{"set": map[string]any{"appearance.terminalFontSize": 33}},
		{"set": map[string]any{"appearance.uiFontScale": 1.1}},
		{"set": map[string]any{"input.selectionAutoCopy": "true"}},
		{"set": map[string]any{"keybinding.closeTab": "p"}},
		{"set": map[string]any{"keybinding.closeTab": map[string]any{"key": "Mod+p"}}},
		{"clear": []string{"appearance.terminalTheme", "dek_envelope"}},
	}
	for _, body := range rejected {
		call := fixture.call(t, http.MethodPut, "/auth/preferences", body, user, user.csrf, nil)
		if call.status != http.StatusBadRequest {
			t.Fatalf("put %v status=%d body=%v", body, call.status, call.body)
		}
	}

	call := fixture.call(t, http.MethodGet, "/auth/preferences", nil, user, "", nil)
	if call.status != http.StatusOK || len(preferenceMap(t, call, "overrides")) != 0 {
		t.Fatalf("rejected writes must not persist: %v", call.body)
	}
}

func TestPreferenceHTTPConcurrentRace(t *testing.T) {
	fixture := newPreferenceFixture(t, AuthOn)
	fixture.initSuperadmin(t, "root", "root-password1")
	users := make([]*accountTestSession, 0, 4)
	for i := 0; i < 4; i++ {
		users = append(users, fixture.createUser(t, fmt.Sprintf("race-%d", i), "race-password1"))
	}

	var wait sync.WaitGroup
	for i, user := range users {
		wait.Add(1)
		go func(i int, user *accountTestSession) {
			defer wait.Done()
			for round := 0; round < 5; round++ {
				body := map[string]any{
					"set": map[string]any{
						"appearance.terminalTheme":    []string{"dark", "light", "interface"}[(i+round)%3],
						"appearance.terminalFontSize": 12 + (i+round)%7,
					},
					"clear": []string{"input.selectionAutoCopy"},
				}
				if call := fixture.call(t, http.MethodPut, "/auth/preferences", body, user, user.csrf, nil); call.status != http.StatusOK {
					t.Errorf("user %d put status=%d body=%v", i, call.status, call.body)
					return
				}
				if call := fixture.call(t, http.MethodGet, "/auth/preferences", nil, user, "", nil); call.status != http.StatusOK {
					t.Errorf("user %d get status=%d", i, call.status)
					return
				}
			}
		}(i, user)
	}
	wait.Wait()

	for i, user := range users {
		call := fixture.call(t, http.MethodGet, "/auth/preferences", nil, user, "", nil)
		overrides := preferenceMap(t, call, "overrides")
		theme, _ := overrides["appearance.terminalTheme"].(string)
		want := []string{"dark", "light", "interface"}[(i+4)%3]
		if theme != want {
			t.Fatalf("user %d final theme=%q want %q overrides=%v", i, theme, want, overrides)
		}
		if _, ok := overrides["input.selectionAutoCopy"]; ok {
			t.Fatalf("user %d clear lost: %v", i, overrides)
		}
	}
}
