package account

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func testPreferences(t *testing.T) *Preferences {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewPreferences(db)
}

func TestPreferenceKeysWhitelist(t *testing.T) {
	keys := PreferenceKeys()
	if len(keys) != 16 {
		t.Fatalf("declared preference keys=%d want 16: %v", len(keys), keys)
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "appearance.") && !strings.HasPrefix(key, "input.") && !strings.HasPrefix(key, "keybinding.") {
			t.Fatalf("unexpected preference key %q", key)
		}
	}
	for _, banned := range []string{"dek", "password", "credential", "secret", "token"} {
		for _, key := range keys {
			if strings.Contains(strings.ToLower(key), banned) {
				t.Fatalf("sensitive preference key %q must not be declared", key)
			}
		}
	}
}

func TestNormalizePreferenceBinding(t *testing.T) {
	valid := map[string]string{
		"Mod+Shift+p":         "Mod+Shift+p",
		"Shift+Mod+P":         "Mod+Shift+p",
		"Mod+1-9":             "Mod+1-9",
		"Escape":              "Escape",
		"F5":                  "F5",
		"F24":                 "F24",
		"Primary+f":           "Primary+f",
		"Ctrl+Alt+Delete":     "Ctrl+Alt+Delete",
		"Meta+ArrowUp":        "Meta+ArrowUp",
		"Mod+\\":              "Mod+\\",
		"Alt+Space":           "Alt+Space",
		"Mod+Tab":             "Mod+Tab",
		"Shift+Enter":         "Shift+Enter",
		"PageUp":              "PageUp",
		"Shift+p":             "Shift+p",
		"Alt+1":               "Alt+1",
		"Mod+Shift+ArrowLeft": "Mod+Shift+ArrowLeft",
	}
	for input, want := range valid {
		got, ok := normalizePreferenceBinding(input)
		if !ok || got != want {
			t.Fatalf("normalizePreferenceBinding(%q)=(%q,%v) want (%q,true)", input, got, ok, want)
		}
	}
	invalid := []string{
		"", "p", "1", "Mod+Primary+p", "Ctrl+Meta+p",
		"Mod+Mod+p", "Hyper+p", "Mod+", "+p", "Mod++", "F0", "F25", "F05",
		"Mod+F25", "mod+p", "Mod+p+q", "Mod+Shift", "1-9+Mod", "é",
	}
	for _, input := range invalid {
		if got, ok := normalizePreferenceBinding(input); ok {
			t.Fatalf("normalizePreferenceBinding(%q)=%q want invalid", input, got)
		}
	}
}

func TestValidatePreferenceValue(t *testing.T) {
	valid := map[string]string{
		"appearance.uiFontPreset":     "14.5",
		"appearance.uiFontScale":      "1.25",
		"appearance.terminalFontSize": "18",
		"appearance.terminalTheme":    `"light"`,
		"input.selectionAutoCopy":     "true",
		"keybinding.closeTab":         `"Mod+Shift+w"`,
		"keybinding.closeTab:null":    "null",
		"keybinding.syncNow":          `"Mod+Shift+s"`,
	}
	for key, want := range valid {
		name := key
		raw := json.RawMessage(want)
		if strings.HasSuffix(key, ":null") {
			name = strings.TrimSuffix(key, ":null")
			raw = json.RawMessage("null")
		}
		got, err := validatePreferenceValue(name, raw)
		if err != nil || got != want {
			t.Fatalf("validatePreferenceValue(%q,%s)=(%q,%v) want (%q,nil)", name, raw, got, err, want)
		}
	}
	invalid := map[string][]string{
		"appearance.uiFontPreset":     {`"13"`, "11", "14", "null", "true"},
		"appearance.uiFontScale":      {"1.1", "2.5", "null", `{"v":1}`},
		"appearance.terminalFontSize": {"11", "19", "13.5", `"13"`, "null"},
		"appearance.terminalTheme":    {`"blue"`, "1", "null", "true"},
		"input.selectionAutoCopy":     {`"true"`, "1", "null"},
		"keybinding.closeTab":         {`"p"`, `"Mod+Mod+p"`, `"Mod+Primary+p"`, "1", "true", `{"key":"Mod+p"}`},
	}
	for key, rawStrings := range invalid {
		for _, rawString := range rawStrings {
			raw := json.RawMessage(rawString)
			if got, err := validatePreferenceValue(key, raw); err == nil {
				t.Fatalf("validatePreferenceValue(%q,%s)=%q want error", key, raw, got)
			}
		}
	}
	for _, key := range []string{"dek_envelope", "password", "ai.models", "prefs.default.appearance.terminalTheme", "keybinding.unknownAction"} {
		if got, err := validatePreferenceValue(key, json.RawMessage(`"x"`)); err == nil {
			t.Fatalf("validatePreferenceValue(%q)=%q want error", key, got)
		}
	}
}

func TestPreferencesDefaultsUpdateAndClear(t *testing.T) {
	ctx := context.Background()
	preferences := testPreferences(t)

	defaults, err := preferences.Defaults(ctx)
	if err != nil || len(defaults) != 0 {
		t.Fatalf("empty defaults=%v err=%v", defaults, err)
	}
	updated, err := preferences.UpdateDefaults(ctx, map[string]json.RawMessage{
		"appearance.terminalTheme": json.RawMessage(`"dark"`),
		"appearance.uiFontScale":   json.RawMessage("1.5"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated["appearance.terminalTheme"]) != `"dark"` || string(updated["appearance.uiFontScale"]) != "1.5" {
		t.Fatalf("updated defaults=%v", updated)
	}
	updated, err = preferences.UpdateDefaults(ctx, nil, []string{"appearance.uiFontScale"})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || string(updated["appearance.terminalTheme"]) != `"dark"` {
		t.Fatalf("defaults after clear=%v", updated)
	}
	if _, err := preferences.UpdateDefaults(ctx, map[string]json.RawMessage{
		"appearance.terminalTheme": json.RawMessage(`"blue"`),
	}, nil); err == nil {
		t.Fatal("invalid default value must be rejected")
	}
	defaults, err = preferences.Defaults(ctx)
	if err != nil || len(defaults) != 1 {
		t.Fatalf("failed update must not write, defaults=%v err=%v", defaults, err)
	}
}

func TestPreferencesUserIsolationAndMerge(t *testing.T) {
	ctx := context.Background()
	preferences := testPreferences(t)
	alice, bob := "01USERALICE0000000000000000", "01USERBOB00000000000000000"

	if _, err := preferences.UpdateUserOverrides(ctx, alice, map[string]json.RawMessage{
		"appearance.terminalTheme": json.RawMessage(`"light"`),
		"keybinding.closeTab":      json.RawMessage("null"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := preferences.UpdateUserOverrides(ctx, bob, map[string]json.RawMessage{
		"appearance.terminalTheme": json.RawMessage(`"interface"`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	aliceOverrides, err := preferences.UserOverrides(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if string(aliceOverrides["appearance.terminalTheme"]) != `"light"` || string(aliceOverrides["keybinding.closeTab"]) != "null" {
		t.Fatalf("alice overrides=%v", aliceOverrides)
	}
	bobOverrides, err := preferences.UserOverrides(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobOverrides) != 1 || string(bobOverrides["appearance.terminalTheme"]) != `"interface"` {
		t.Fatalf("bob overrides=%v", bobOverrides)
	}

	if _, err := preferences.UpdateDefaults(ctx, map[string]json.RawMessage{
		"appearance.terminalTheme": json.RawMessage(`"dark"`),
		"keybinding.closeTab":      json.RawMessage(`"Mod+Shift+w"`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	defaults, err := preferences.Defaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	effective := MergePreferences(defaults, aliceOverrides)
	if string(effective["appearance.terminalTheme"]) != `"light"` {
		t.Fatalf("override must win, effective=%v", effective)
	}
	if value, ok := effective["keybinding.closeTab"]; !ok || string(value) != "null" {
		t.Fatalf("explicit null override must win, effective=%v", effective)
	}
	effective = MergePreferences(defaults, bobOverrides)
	if string(effective["appearance.terminalTheme"]) != `"interface"` || string(effective["keybinding.closeTab"]) != `"Mod+Shift+w"` {
		t.Fatalf("effective=%v", effective)
	}

	aliceOverrides, err = preferences.UpdateUserOverrides(ctx, alice, nil, []string{"appearance.terminalTheme", "keybinding.closeTab"})
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceOverrides) != 0 {
		t.Fatalf("alice overrides after clear=%v", aliceOverrides)
	}
	effective = MergePreferences(defaults, aliceOverrides)
	if string(effective["appearance.terminalTheme"]) != `"dark"` || string(effective["keybinding.closeTab"]) != `"Mod+Shift+w"` {
		t.Fatalf("cleared override must inherit default, effective=%v", effective)
	}

	bobOverrides, err = preferences.UserOverrides(ctx, bob)
	if err != nil || len(bobOverrides) != 1 {
		t.Fatalf("alice clear must not touch bob, overrides=%v err=%v", bobOverrides, err)
	}
}
