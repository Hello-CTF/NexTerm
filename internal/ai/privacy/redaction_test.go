package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"strings"
	"testing"
)

type detectorFunc func(context.Context, string) ([]Span, error)

func (f detectorFunc) Detect(ctx context.Context, text string) ([]Span, error) { return f(ctx, text) }

func exchangeFor(t *testing.T, secret string) *Exchange {
	t.Helper()
	e, err := NewExchange(detectorFunc(func(_ context.Context, text string) ([]Span, error) {
		var spans []Span
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], secret)
			if i < 0 {
				break
			}
			i += from
			spans = append(spans, Span{Start: i, End: i + len(secret)})
			from = i + len(secret)
		}
		return spans, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestExchangeProtectRestoreAndIsolation(t *testing.T) {
	secret := "张伟@example.com"
	e := exchangeFor(t, secret)
	text := "联系 " + secret + "，再次联系 " + secret
	protected, err := e.Protect(t.Context(), text)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, secret) || e.Restore(protected) != text {
		t.Fatalf("round trip failed")
	}
	marker := e.markers[secret]
	if strings.Count(protected, marker) != 2 {
		t.Fatal("repeated value did not reuse its placeholder")
	}
	other := exchangeFor(t, secret)
	if other.Restore(protected) != protected {
		t.Fatal("cross-request restoration")
	}
	if e.Restore("<NX_GUARD_unknown_1>") != "<NX_GUARD_unknown_1>" {
		t.Fatal("unknown placeholder changed")
	}
}

func TestRestoreEveryStreamBoundary(t *testing.T) {
	e := exchangeFor(t, "secret")
	protected, err := e.Protect(t.Context(), "prefix secret suffix")
	if err != nil {
		t.Fatal(err)
	}
	for split := 0; split <= len(protected); split++ {
		r := e.NewRestorer()
		got := r.Push(protected[:split], false) + r.Push(protected[split:], false) + r.Push("", true)
		if got != "prefix secret suffix" {
			t.Fatalf("split %d: %q", split, got)
		}
	}
	r := e.NewRestorer()
	var result strings.Builder
	for _, b := range []byte(protected) {
		result.WriteString(r.Push(string(b), false))
	}
	result.WriteString(r.Push("", true))
	if result.String() != "prefix secret suffix" {
		t.Fatal(result.String())
	}
}

func TestJSONRoundTripEscapingAndNumbers(t *testing.T) {
	secret := "密码\"\\\n"
	e := exchangeFor(t, secret)
	raw, _ := json.Marshal(map[string]any{"command": secret, "id": json.Number("9007199254740993")})
	protected, err := TransformJSON(string(raw), func(s string) (string, error) { return e.Protect(t.Context(), s) })
	if err != nil {
		t.Fatal(err)
	}
	restored, err := TransformJSON(protected, func(s string) (string, error) { return e.Restore(s), nil })
	if err != nil {
		t.Fatal(err)
	}
	if restored != string(raw) {
		t.Fatalf("got %s", restored)
	}
	if _, err := TransformJSON(`{"x":`, func(s string) (string, error) { return s, nil }); err == nil {
		t.Fatal("accepted incomplete JSON")
	}
}

func TestDetectionFailureAndInvalidOffsetsBlock(t *testing.T) {
	for _, f := range []detectorFunc{
		func(context.Context, string) ([]Span, error) { return nil, errors.New("unavailable") },
		func(context.Context, string) ([]Span, error) { return []Span{{Start: 1, End: 3}}, nil },
		func(context.Context, string) ([]Span, error) { return []Span{{Start: 0, End: 99}}, nil },
	} {
		e, err := NewExchange(f)
		if err != nil {
			t.Fatal(err)
		}
		if text, err := e.Protect(t.Context(), "秘密"); err == nil || text != "" {
			t.Fatal("failed open")
		}
	}
}

func TestOverlappingEntitiesCoverTheirUnion(t *testing.T) {
	e, _ := NewExchange(detectorFunc(func(context.Context, string) ([]Span, error) {
		return []Span{{Start: 2, End: 6}, {Start: 0, End: 4}}, nil
	}))
	protected, err := e.Protect(t.Context(), "abcdef safe")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(protected, "abcdef") || !strings.HasSuffix(protected, " safe") || e.Restore(protected) != "abcdef safe" {
		t.Fatal("overlap lost data")
	}
}

func TestStableMarkersAcrossTurnsAndDetectionOrder(t *testing.T) {
	master := [32]byte{1, 2, 3}
	key := deriveScopeKey(master, "alice", "conversation-1")
	detector := detectorFunc(func(_ context.Context, s string) ([]Span, error) {
		var result []Span
		for _, value := range []string{"first-secret", "second-secret"} {
			if start := strings.Index(s, value); start >= 0 {
				result = append(result, Span{Start: start, End: start + len(value)})
			}
		}
		return result, nil
	})
	first, _ := NewScopedExchange(detector, key)
	one, err := first.Protect(t.Context(), "first-secret")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewScopedExchange(detector, key)
	if _, err := second.Protect(t.Context(), "second-secret"); err != nil {
		t.Fatal(err)
	}
	two, err := second.Protect(t.Context(), "first-secret")
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("placeholder changed across rounds")
	}
	if first.Restore(second.markers["second-secret"]) != second.markers["second-secret"] {
		t.Fatal("request learned another request's mapping")
	}
	for _, scope := range [][2]string{{"alice", "conversation-2"}, {"bob", "conversation-1"}} {
		other, _ := NewScopedExchange(detector, deriveScopeKey(master, scope[0], scope[1]))
		protected, err := other.Protect(t.Context(), "first-secret")
		if err != nil {
			t.Fatal(err)
		}
		if protected == one || other.Restore(one) != one {
			t.Fatal("scope isolation failed")
		}
	}
}

func TestStableMarkersPreserveQuotedHistory(t *testing.T) {
	key := [32]byte{7}
	detector := exchangeFor(t, "secret").detector
	first, _ := NewScopedExchange(detector, key)
	marker, err := first.Protect(t.Context(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, quoted := range []string{marker, html.EscapeString(marker), strings.Trim(marker, "<>")} {
		second, _ := NewScopedExchange(detector, key)
		protected, err := second.Protect(t.Context(), quoted+" then secret")
		if err != nil {
			t.Fatal(err)
		}
		if protected != quoted+" then "+marker {
			t.Fatalf("history changed: %q", protected)
		}
	}
}

func TestStreamingRestorationKeepsEscapingAndLeftBoundaries(t *testing.T) {
	secret := `Zoë "Z" & Co`
	e := exchangeFor(t, secret)
	marker, err := e.Protect(t.Context(), secret)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Trim(marker, "<>")
	for _, input := range []string{
		"before " + marker + " after",
		"before " + html.EscapeString(marker) + " after",
		"before %3C" + body + "%3e after",
		`before \u003c` + body + `\u003e after`,
		"before " + body + " after",
		"X" + body + " after",
		"before " + body + "Z",
		`\\u003c` + body + `\u003e`,
		`\\\u003c` + body + `\u003e`,
	} {
		want := e.Restore(input)
		for split := 0; split <= len(input); split++ {
			r := e.NewRestorer()
			got := r.Push(input[:split], false) + r.Push(input[split:], false) + r.Push("", true)
			if got != want {
				t.Fatalf("split %d in %q: %q, want %q", split, input, got, want)
			}
		}
		r := e.NewRestorer()
		var result strings.Builder
		for _, b := range []byte(input) {
			result.WriteString(r.Push(string(b), false))
		}
		result.WriteString(r.Push("", true))
		if result.String() != want {
			t.Fatalf("byte stream %q: %q, want %q", input, result.String(), want)
		}
	}
}

func TestRestoreDoesNotReinterpretInsertedSecret(t *testing.T) {
	const first = "<PRIVATE_EMAIL_1234567890abcdef>"
	const second = "<SECRET_1234567890abcdef>"
	e := exchangeFor(t, "unused")
	e.values[first] = second
	e.values[second] = "another secret"
	if got := e.Restore(first); got != second {
		t.Fatalf("recursive restoration: %q", got)
	}
	r := e.NewRestorer()
	if got := r.Push(first, false) + r.Push("", true); got != second {
		t.Fatalf("recursive streaming restoration: %q", got)
	}
}

func TestPlaceholderFragmentsAreNotRedactedAgain(t *testing.T) {
	key := [32]byte{9}
	allocator := newPlaceholderAllocator(key)
	for _, kind := range []Kind{KindEmail, KindPhone, KindAccount, KindPaymentCard, KindIPAddress, KindURL, KindCommonSecret, KindAddress, KindDate, KindPerson} {
		marker, err := allocator.allocate(kind, "original")
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range []string{marker, marker[1 : len(marker)-1], marker[len(marker)-placeholderTokenHexLength-1 : len(marker)-1]} {
			text := "before " + marker + " after"
			e, _ := NewScopedExchange(detectorFunc(func(context.Context, string) ([]Span, error) {
				start := strings.Index(text, fragment)
				return []Span{{Start: start, End: start + len(fragment), Label: string(KindCommonSecret)}}, nil
			}), key)
			got, err := e.Protect(t.Context(), text)
			if err != nil || got != text {
				t.Fatalf("placeholder rewritten: %q, %v", got, err)
			}
		}
	}
	e := exchangeFor(t, "actual secret")
	got, err := e.Protect(t.Context(), "<SECRET actual secret>")
	if err != nil || strings.Contains(got, "actual secret") {
		t.Fatalf("arbitrary tag bypass: %q, %v", got, err)
	}
}

func TestTransformJSONRejectsAmbiguousAndUnboundedInputs(t *testing.T) {
	for _, input := range []string{
		`{"x":"first","x":"second"}`,
		`{"x":"first","\u0078":"second"}`,
		`[{"nested":1,"nested":2}]`,
		strings.Repeat("[", maxJSONDepth+1) + "0" + strings.Repeat("]", maxJSONDepth+1),
		"{\"value\":\"\xff\"}",
	} {
		called := false
		_, err := TransformJSON(input, func(value string) (string, error) { called = true; return value, nil })
		if err == nil || called {
			t.Fatalf("unsafe JSON was transformed: %q", input)
		}
	}
}

func FuzzStreamingRestoration(f *testing.F) {
	const marker = "<PRIVATE_PERSON_1234567890abcdef>"
	body := strings.Trim(marker, "<>")
	for _, input := range []string{marker, "X" + body, " " + body + "Z", html.EscapeString(marker), `\\u003c` + body + `\u003e`, `\\\u003c` + body + `\u003e`, "%3C" + body + "%3E"} {
		f.Add(input, uint8(1))
	}
	f.Fuzz(func(t *testing.T, input string, width uint8) {
		restorer := NewTextRestorer([]Redaction{{Placeholder: marker, Value: `a "quoted" & secret`}}, ValuePlain)
		want, _, _ := restorer.Restore(input, true)
		stream := &Restorer{restorer: restorer}
		var out strings.Builder
		step := int(width) + 1
		for start := 0; start < len(input); start += step {
			out.WriteString(stream.Push(input[start:min(start+step, len(input))], false))
		}
		out.WriteString(stream.Push("", true))
		if out.String() != want {
			t.Fatalf("width %d input %q: %q, want %q", width, input, out.String(), want)
		}
	})
}

func TestKnownValuesPropagateAcrossFields(t *testing.T) {
	const secret = "hunter22"
	e, err := NewExchange(detectorFunc(func(_ context.Context, text string) ([]Span, error) {
		// Only the first mention carries enough context for the detector.
		const prefix = "my password: "
		if strings.HasPrefix(text, prefix+secret) {
			return []Span{{Start: len(prefix), End: len(prefix) + len(secret), Label: string(KindCommonSecret)}}, nil
		}
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.Protect(t.Context(), "my password: "+secret)
	if err != nil {
		t.Fatal(err)
	}
	text, err := e.Protect(t.Context(), "run echo "+secret+" now")
	if err != nil {
		t.Fatal(err)
	}
	args, err := e.ProtectJSON(t.Context(), `{"command":"echo `+secret+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{first, text, args} {
		if strings.Contains(protected, secret) {
			t.Fatalf("known value leaked: %q", protected)
		}
	}
	if e.Restore(text) != "run echo "+secret+" now" {
		t.Fatalf("restore = %q", e.Restore(text))
	}
	restored, err := e.RestoreJSON(args)
	if err != nil || restored != `{"command":"echo `+secret+`"}` {
		t.Fatalf("restore JSON = %q, %v", restored, err)
	}
}

type identifiedDetector struct {
	detectorFunc
	id string
}

func (d identifiedDetector) detectionIdentity() string { return d.id }

func TestDetectionCacheReusesFindingsWithinScopeOnly(t *testing.T) {
	calls := 0
	detector := identifiedDetector{id: t.Name(), detectorFunc: func(_ context.Context, text string) ([]Span, error) {
		calls++
		return []Span{{Start: 0, End: 6, Label: string(KindCommonSecret)}}, nil
	}}
	run := func(key [32]byte) string {
		e, err := NewScopedExchange(detector, key)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := e.Protect(t.Context(), "secret text")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Protect(t.Context(), "secret text"); err != nil {
			t.Fatal(err)
		}
		return protected
	}
	first := run([32]byte{9})
	if second := run([32]byte{9}); second != first || calls != 1 {
		t.Fatalf("calls = %d, outputs %q / %q", calls, first, second)
	}
	run([32]byte{10})
	if calls != 2 {
		t.Fatalf("cache crossed scopes: calls = %d", calls)
	}
	cache := newDetectionCache(2)
	for i := range 3 {
		cache.put([32]byte{byte(i)}, []Span{{End: i + 1}})
	}
	if _, ok := cache.get([32]byte{0}); ok || cache.order.Len() != 2 {
		t.Fatal("detection cache is not bounded")
	}
}
