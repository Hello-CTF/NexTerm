package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestStructuredNumbersRestoreWithoutLosingTypeOrPrecision(t *testing.T) {
	const number = "9007199254740993123456789"
	e := exchangeFor(t, number)
	raw := `{"number":` + number + `,"string":"` + number + `","ordinary":17,"nested":[` + number + `]}`
	protected, err := e.ProtectJSON(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, number) {
		t.Fatal("sensitive number escaped protection")
	}
	restored, err := e.RestoreJSON(protected)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	decoder := json.NewDecoder(strings.NewReader(restored))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["number"] != json.Number(number) || got["string"] != number || got["ordinary"] != json.Number("17") || got["nested"].([]any)[0] != json.Number(number) {
		t.Fatalf("types/values changed: %#v", got)
	}
	var encoded map[string]any
	if err := json.Unmarshal([]byte(protected), &encoded); err != nil {
		t.Fatal(err)
	}
	numberMarker, stringMarker := encoded["number"].(string), encoded["string"].(string)
	if numberMarker == stringMarker {
		t.Fatal("typed values share a marker")
	}
	command, _ := json.Marshal(map[string]string{"command": "echo " + numberMarker})
	result, err := e.RestoreJSON(string(command))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(result), &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["command"] != "echo "+number {
		t.Fatalf("embedded marker changed type: %s", result)
	}
}

func TestStructuredNumericDetectionRetainsContextAndExpandsPartialFinding(t *testing.T) {
	const raw = `{"phone":13812345678,"plain":9}`
	e, err := NewScopedExchange(detectorFunc(func(_ context.Context, text string) ([]Span, error) {
		if text != raw {
			return nil, nil
		}
		at := strings.Index(text, "13812345678")
		return []Span{{Start: at + 3, End: at + 6, Label: "phone", Score: 1}}, nil
	}), [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := e.ProtectJSON(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, "13812345678") || strings.Contains(protected, ":138") {
		t.Fatal("partial numeric finding escaped")
	}
	restored, err := e.RestoreJSON(protected)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.Number
	if err := json.Unmarshal([]byte(restored), &got); err != nil {
		t.Fatal(err)
	}
	if got["phone"] != "13812345678" || got["plain"] != "9" {
		t.Fatalf("round trip %s", restored)
	}
}

func TestStructuredSensitiveKeysFailClosedIncludingEscapedKeys(t *testing.T) {
	for _, raw := range []string{`{"alice@example.com":"value"}`, `{"alice\u0040example.com":"value"}`} {
		e := exchangeFor(t, "alice@example.com")
		if _, err := e.ProtectJSON(t.Context(), raw); err == nil {
			t.Fatal("sensitive map key was sent")
		}
	}
}

func TestStructuredNumbersStableAcrossExchangesAndScopeIsolated(t *testing.T) {
	detector := detectorFunc(func(_ context.Context, text string) ([]Span, error) {
		i := strings.Index(text, "13812345678")
		if i < 0 {
			return nil, nil
		}
		return []Span{{Start: i, End: i + 11, Label: "phone"}}, nil
	})
	a, _ := NewScopedExchange(detector, [32]byte{1})
	b, _ := NewScopedExchange(detector, [32]byte{1})
	other, _ := NewScopedExchange(detector, [32]byte{2})
	raw := `{"phone":13812345678}`
	first, err := a.ProtectJSON(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := b.ProtectJSON(t.Context(), raw)
	foreign, _ := other.ProtectJSON(t.Context(), raw)
	if first != second || first == foreign {
		t.Fatal("numeric marker scope/stability mismatch")
	}
	restored, err := other.RestoreJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	if restored != first {
		t.Fatal("foreign scope restored a numeric marker")
	}
}

func TestStructuredInvalidAndCancelledInputsFailClosed(t *testing.T) {
	e := exchangeFor(t, "secret")
	for _, raw := range []string{`{"a":1,"a":2}`, string([]byte{'"', 0xff, '"'})} {
		if _, err := e.ProtectJSON(t.Context(), raw); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.ProtectJSON(ctx, `{"a":1}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestStructuredStringFindingsRetainFieldContextAndEscapes(t *testing.T) {
	const raw = `{"secret":"prefix value\"\\\n suffix"}`
	e, err := NewScopedExchange(detectorFunc(func(_ context.Context, text string) ([]Span, error) {
		if text != raw {
			return nil, nil
		}
		at := strings.Index(text, `value\"\\\n`)
		return []Span{{Start: at, End: at + len(`value\"\\\n`), Label: "common_secret"}}, nil
	}), [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := e.ProtectJSON(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, "value") {
		t.Fatal("contextual string detection was discarded")
	}
	restored, err := e.RestoreJSON(protected)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal([]byte(restored), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["secret"] != want.(map[string]any)["secret"] {
		t.Fatalf("string escapes changed: %s", restored)
	}
}

func TestStructuredEscapedSurrogateFindingPreservesCharacter(t *testing.T) {
	const raw = `{"value":"before \ud83d\ude00 after"}`
	e, _ := NewScopedExchange(detectorFunc(func(_ context.Context, text string) ([]Span, error) {
		if text != raw {
			return nil, nil
		}
		at := strings.Index(text, `\ud83d`)
		return []Span{{Start: at + 2, End: at + 4, Label: "common_secret"}}, nil
	}), [32]byte{1})
	protected, err := e.ProtectJSON(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := e.RestoreJSON(protected)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(restored), &got); err != nil {
		t.Fatal(err)
	}
	if got["value"] != "before 😀 after" {
		t.Fatalf("split surrogate: %s", restored)
	}
}

func TestStructuredEmptyAndNonJSONArgumentsPassThroughSafely(t *testing.T) {
	e := exchangeFor(t, "secret-token")
	for _, raw := range []string{"", "  \n\t"} {
		protected, err := e.ProtectJSON(t.Context(), raw)
		if err != nil || protected != raw {
			t.Fatalf("empty arguments: %q, %v", protected, err)
		}
		restored, err := e.RestoreJSON(raw)
		if err != nil || restored != raw {
			t.Fatalf("empty restore: %q, %v", restored, err)
		}
	}
	const raw = `{"cmd": "echo secret-token"`
	protected, err := e.ProtectJSON(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, "secret-token") {
		t.Fatalf("non-JSON arguments leaked: %q", protected)
	}
	restored, err := e.RestoreJSON(protected)
	if err != nil || restored != raw {
		t.Fatalf("non-JSON restore: %q, %v", restored, err)
	}
}
