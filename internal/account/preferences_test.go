package account

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
)

func testPreferences(t *testing.T) *Preferences {
	t.Helper()
	db := dbtest.NewFixture(t).OpenStore(t)
	return NewPreferences(db)
}

func TestPreferenceKeysWhitelist(t *testing.T) {
	keys := PreferenceKeys()
	declared := make(map[string]bool, len(keys))
	for _, key := range keys {
		if key == "" {
			t.Fatal("empty preference key must not be declared")
		}
		if declared[key] {
			t.Fatalf("duplicate preference key %q", key)
		}
		declared[key] = true
		if !strings.HasPrefix(key, "appearance.") && !strings.HasPrefix(key, "input.") && !strings.HasPrefix(key, "keybinding.") && !strings.HasPrefix(key, "terminal.") && !strings.HasPrefix(key, "project.") {
			t.Fatalf("unexpected preference key %q", key)
		}
	}
	for _, key := range []string{
		"appearance.uiFontPreset",
		"appearance.uiFontScale",
		"appearance.terminalFontSize",
		"appearance.terminalFontFamily",
		"appearance.terminalTheme",
		"appearance.terminalContrast",
		"appearance.themeMode",
		"input.selectionAutoCopy",
		"terminal.closeAction",
		"terminal.defaultMode",
		"terminal.gpuAcceleration",
		"keybinding.commandPalette",
		"keybinding.quickConnect",
		"keybinding.newTerminal",
		"keybinding.nextWorkspace",
		"keybinding.previousWorkspace",
		"keybinding.nextTab",
		"keybinding.previousTab",
		"keybinding.focusUpperPane",
		"keybinding.focusLowerPane",
		"keybinding.toggleSidebar",
		"keybinding.toggleAiSidebar",
		"keybinding.closeTab",
		"keybinding.toggleSplit",
		"keybinding.splitRight",
		"keybinding.splitDown",
		"keybinding.focusPaneLeft",
		"keybinding.focusPaneRight",
		"keybinding.focusPaneUp",
		"keybinding.focusPaneDown",
		"keybinding.switchTab",
		"keybinding.terminalSearch",
		"keybinding.reclaimTakeover",
		"keybinding.lockVault",
	} {
		if !declared[key] {
			t.Errorf("required preference key %q is not declared", key)
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
		"appearance.uiFontPreset":       "15.5",
		"appearance.uiFontScale":        "1.25",
		"appearance.terminalFontSize":   "32",
		"appearance.terminalFontFamily": `"jetbrains-mono"`,
		"appearance.terminalTheme":      `"light"`,
		"appearance.terminalContrast":   `"maximum"`,
		"appearance.themeMode":          `"system"`,
		"input.selectionAutoCopy":       "true",
		"terminal.closeAction":          `"kill"`,
		"terminal.defaultMode":          `"regular"`,
		"terminal.gpuAcceleration":      `"off"`,
		"keybinding.closeTab":           `"Mod+Shift+w"`,
		"keybinding.closeTab:null":      "null",
		"keybinding.reclaimTakeover":    `"Mod+Shift+s"`,
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
	for _, value := range []string{"cascadia", "system", "jetbrains-mono", "fira-code", "source-code-pro", "menlo", "consolas"} {
		got, err := validatePreferenceValue("appearance.terminalFontFamily", json.RawMessage(strconv.Quote(value)))
		if err != nil || got != strconv.Quote(value) {
			t.Errorf("terminal font %q=(%q,%v)", value, got, err)
		}
	}
	for _, value := range []string{"standard", "high", "maximum"} {
		got, err := validatePreferenceValue("appearance.terminalContrast", json.RawMessage(strconv.Quote(value)))
		if err != nil || got != strconv.Quote(value) {
			t.Errorf("terminal contrast %q=(%q,%v)", value, got, err)
		}
	}
	invalid := map[string][]string{
		"appearance.uiFontPreset":       {`"13"`, "7.5", "13.25", "32.5", "null", "true"},
		"appearance.uiFontScale":        {"1.1", "2.5", "null", `{"v":1}`},
		"appearance.terminalFontSize":   {"7", "33", "13.5", `"13"`, "null"},
		"appearance.terminalFontFamily": {`"downloaded-font"`, `"Cascadia Mono"`, "1", "null", "true"},
		"appearance.terminalTheme":      {`"blue"`, "1", "null", "true"},
		"appearance.terminalContrast":   {`"extreme"`, "1", "null", "true"},
		"appearance.themeMode":          {`"blue"`, "1", "null", "true"},
		"input.selectionAutoCopy":       {`"true"`, "1", "null"},
		"terminal.closeAction":          {`"background"`, "1", "null", "true"},
		"terminal.defaultMode":          {`"auto"`, `"persistent"`, "1", "null", "true"},
		"terminal.gpuAcceleration":      {`"webgl"`, `"Auto"`, "1", "null", "true"},
		"keybinding.closeTab":           {`"p"`, `"Mod+Mod+p"`, `"Mod+Primary+p"`, "1", "true", `{"key":"Mod+p"}`},
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

func TestNavigationKeybindingPreferences(t *testing.T) {
	tests := []struct {
		key   string
		input string
		want  string
	}{
		{key: "keybinding.nextWorkspace", input: `"Alt+Mod+ArrowRight"`, want: `"Mod+Alt+ArrowRight"`},
		{key: "keybinding.previousWorkspace", input: `"Alt+Mod+ArrowLeft"`, want: `"Mod+Alt+ArrowLeft"`},
		{key: "keybinding.nextTab", input: `"Shift+Mod+]"`, want: `"Mod+Shift+]"`},
		{key: "keybinding.previousTab", input: `"Shift+Mod+["`, want: `"Mod+Shift+["`},
		{key: "keybinding.focusUpperPane", input: `"Alt+Mod+ArrowUp"`, want: `"Mod+Alt+ArrowUp"`},
		{key: "keybinding.focusLowerPane", input: `"Alt+Mod+ArrowDown"`, want: `"Mod+Alt+ArrowDown"`},
	}
	for _, test := range tests {
		t.Run("validate "+test.key, func(t *testing.T) {
			got, err := validatePreferenceValue(test.key, json.RawMessage(test.input))
			if err != nil || got != test.want {
				t.Fatalf("validatePreferenceValue(%q,%s)=(%q,%v) want (%q,nil)", test.key, test.input, got, err, test.want)
			}
		})
	}

	t.Run("set get and clear user overrides", func(t *testing.T) {
		ctx := context.Background()
		preferences := testPreferences(t)
		const userID = "01USERNAVIGATION00000000000"
		set := make(map[string]json.RawMessage, len(tests))
		for _, test := range tests {
			set[test.key] = json.RawMessage(test.input)
		}
		updated, err := preferences.UpdateUserOverrides(ctx, userID, set, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(updated) != len(tests) {
			t.Fatalf("updated override count=%d want %d: %v", len(updated), len(tests), updated)
		}
		for _, test := range tests {
			if got := string(updated[test.key]); got != test.want {
				t.Errorf("updated override %q=%q want %q", test.key, got, test.want)
			}
		}

		stored, err := preferences.UserOverrides(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) != len(tests) {
			t.Fatalf("stored override count=%d want %d: %v", len(stored), len(tests), stored)
		}
		for _, test := range tests {
			if got := string(stored[test.key]); got != test.want {
				t.Errorf("stored override %q=%q want %q", test.key, got, test.want)
			}
		}

		clear := make([]string, 0, len(tests))
		for _, test := range tests {
			clear = append(clear, test.key)
		}
		updated, err = preferences.UpdateUserOverrides(ctx, userID, nil, clear)
		if err != nil {
			t.Fatal(err)
		}
		if len(updated) != 0 {
			t.Fatalf("overrides after clear=%v", updated)
		}
		stored, err = preferences.UserOverrides(ctx, userID)
		if err != nil || len(stored) != 0 {
			t.Fatalf("stored overrides after clear=%v err=%v", stored, err)
		}
	})
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

func TestPreferencesTerminalDefaultModeIsolation(t *testing.T) {
	preferences := testPreferences(t)
	ctx := t.Context()
	if _, err := preferences.UpdateDefaults(ctx, map[string]json.RawMessage{"terminal.defaultMode": json.RawMessage(`"durable"`)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := preferences.UpdateUserOverrides(ctx, "alice", map[string]json.RawMessage{"terminal.defaultMode": json.RawMessage(`"regular"`)}, nil); err != nil {
		t.Fatal(err)
	}
	defaults, err := preferences.Defaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, want string }{{"alice", `"regular"`}, {"bob", `"durable"`}} {
		overrides, err := preferences.UserOverrides(ctx, user.id)
		if err != nil {
			t.Fatal(err)
		}
		if effective := MergePreferences(defaults, overrides); string(effective["terminal.defaultMode"]) != user.want {
			t.Fatalf("%s effective mode = %s, want %s", user.id, effective["terminal.defaultMode"], user.want)
		}
	}
	if _, err := preferences.UpdateUserOverrides(ctx, "alice", map[string]json.RawMessage{"terminal.defaultMode": json.RawMessage(`"auto"`)}, nil); err == nil {
		t.Fatal("invalid default mode was accepted")
	}
	overrides, err := preferences.UpdateUserOverrides(ctx, "alice", nil, []string{"terminal.defaultMode"})
	if err != nil {
		t.Fatal(err)
	}
	if effective := MergePreferences(defaults, overrides); string(effective["terminal.defaultMode"]) != `"durable"` {
		t.Fatalf("cleared override did not restore durable default: %v", effective)
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

func TestPreferencesTerminalFontAndContrastIsolation(t *testing.T) {
	ctx := context.Background()
	preferences := testPreferences(t)
	alice, bob := "01USERFONTALICE000000000000", "01USERFONTBOB00000000000000"

	if _, err := preferences.UpdateDefaults(ctx, map[string]json.RawMessage{
		"appearance.terminalFontFamily": json.RawMessage(`"menlo"`),
		"appearance.terminalContrast":   json.RawMessage(`"standard"`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := preferences.UpdateUserOverrides(ctx, alice, map[string]json.RawMessage{
		"appearance.terminalFontFamily": json.RawMessage(`"jetbrains-mono"`),
		"appearance.terminalContrast":   json.RawMessage(`"high"`),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := preferences.UpdateUserOverrides(ctx, bob, map[string]json.RawMessage{
		"appearance.terminalFontFamily": json.RawMessage(`"fira-code"`),
		"appearance.terminalContrast":   json.RawMessage(`"maximum"`),
	}, nil); err != nil {
		t.Fatal(err)
	}

	defaults, err := preferences.Defaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	aliceOverrides, err := preferences.UserOverrides(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	effective := MergePreferences(defaults, aliceOverrides)
	if string(effective["appearance.terminalFontFamily"]) != `"jetbrains-mono"` || string(effective["appearance.terminalContrast"]) != `"high"` {
		t.Fatalf("alice effective preferences=%v", effective)
	}
	aliceOverrides, err = preferences.UpdateUserOverrides(ctx, alice, nil, []string{"appearance.terminalFontFamily", "appearance.terminalContrast"})
	if err != nil || len(aliceOverrides) != 0 {
		t.Fatalf("alice overrides after clear=%v err=%v", aliceOverrides, err)
	}
	effective = MergePreferences(defaults, aliceOverrides)
	if string(effective["appearance.terminalFontFamily"]) != `"menlo"` || string(effective["appearance.terminalContrast"]) != `"standard"` {
		t.Fatalf("alice cleared preferences must inherit defaults=%v", effective)
	}

	bobOverrides, err := preferences.UserOverrides(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if string(bobOverrides["appearance.terminalFontFamily"]) != `"fira-code"` || string(bobOverrides["appearance.terminalContrast"]) != `"maximum"` {
		t.Fatalf("alice clear must not touch bob=%v", bobOverrides)
	}
}
