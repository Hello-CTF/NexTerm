package account

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestTerminalSchemePreferences(t *testing.T) {
	for _, key := range []string{"appearance.terminalSchemeDark", "appearance.terminalSchemeLight"} {
		for _, id := range []string{"nexterm-dark", "catppuccin-mocha", "custom-abc123", "future-scheme-2"} {
			if got, err := validatePreferenceValue(key, json.RawMessage(strconv.Quote(id))); err != nil || got != strconv.Quote(id) {
				t.Fatalf("%s=%q: (%q,%v)", key, id, got, err)
			}
		}
		for _, raw := range []string{`""`, `"Nord"`, `"-dark"`, `"a b"`, `"` + strings.Repeat("a", 49) + `"`, `1`, `null`} {
			if got, err := validatePreferenceValue(key, json.RawMessage(raw)); err == nil {
				t.Fatalf("%s=%s accepted as %q", key, raw, got)
			}
		}
	}
	if got, err := validatePreferenceValue("appearance.accent", json.RawMessage(`"teal"`)); err != nil || got != `"teal"` {
		t.Fatalf("accent=(%q,%v)", got, err)
	}
	for _, raw := range []string{`"red"`, `"#ff0000"`, `""`} {
		if _, err := validatePreferenceValue("appearance.accent", json.RawMessage(raw)); err == nil {
			t.Fatalf("accent %s must be rejected", raw)
		}
	}
}

func TestTerminalCustomSchemesPreference(t *testing.T) {
	ansi := make([]string, 16)
	for i := range ansi {
		ansi[i] = "#0A0B0C"
	}
	scheme := func(id, name string) map[string]any {
		return map[string]any{"id": id, "name": name, "background": "#101217", "foreground": "#C6CBD6", "ansi": ansi}
	}
	encode := func(value any) json.RawMessage {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	withCursor := scheme("custom-a1", "<Mine>")
	withCursor["cursor"] = "#FFFFFF"
	got, err := validatePreferenceValue("appearance.terminalCustomSchemes", encode([]any{withCursor}))
	if err != nil {
		t.Fatal(err)
	}
	lowerANSI := `"#0a0b0c"` + strings.Repeat(`,"#0a0b0c"`, 15)
	want := `[{"id":"custom-a1","name":"<Mine>","background":"#101217","foreground":"#c6cbd6","cursor":"#ffffff","ansi":[` + lowerANSI + `]}]`
	if got != want {
		t.Fatalf("canonical=%s\nwant=%s", got, want)
	}
	if got, err := validatePreferenceValue("appearance.terminalCustomSchemes", json.RawMessage(`[]`)); err != nil || got != `[]` {
		t.Fatalf("empty list=(%q,%v)", got, err)
	}

	full := make([]any, maxTerminalCustomSchemes)
	for i := range full {
		full[i] = scheme("custom-"+strconv.Itoa(i), strings.Repeat("名", maxTerminalSchemeName))
	}
	if _, err := validatePreferenceValue("appearance.terminalCustomSchemes", encode(full)); err != nil {
		t.Fatalf("%d schemes with maximal names must be accepted: %v", maxTerminalCustomSchemes, err)
	}

	shortANSI := scheme("custom-a", "x")
	shortANSI["ansi"] = ansi[:15]
	badColor := scheme("custom-a", "x")
	badColor["background"] = "red"
	badCursor := scheme("custom-a", "x")
	badCursor["cursor"] = "#fff"
	unknown := scheme("custom-a", "x")
	unknown["bold"] = "#ffffff"
	invalid := map[string]json.RawMessage{
		"null":           json.RawMessage(`null`),
		"object":         encode(scheme("custom-a", "x")),
		"too many":       encode(append(full, scheme("custom-extra", "x"))),
		"duplicate id":   encode([]any{scheme("custom-a", "x"), scheme("custom-a", "y")}),
		"builtin id":     encode([]any{scheme("nord", "x")}),
		"empty name":     encode([]any{scheme("custom-a", " ")}),
		"long name":      encode([]any{scheme("custom-a", strings.Repeat("n", maxTerminalSchemeName+1))}),
		"control name":   encode([]any{scheme("custom-a", "a\tb")}),
		"15 ansi colors": encode([]any{shortANSI}),
		"bad color":      encode([]any{badColor}),
		"short cursor":   encode([]any{badCursor}),
		"unknown field":  encode([]any{unknown}),
		"trailing":       json.RawMessage(`[] []`),
	}
	for name, raw := range invalid {
		if got, err := validatePreferenceValue("appearance.terminalCustomSchemes", raw); err == nil {
			t.Fatalf("%s accepted as %q", name, got)
		}
	}
}
