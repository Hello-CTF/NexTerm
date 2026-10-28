package inference

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestValidateSpansRejectsBadOffsetsAndScores(t *testing.T) {
	for _, span := range []Span{{Start: 1, End: 3, Label: "email", Score: 1}, {Start: 0, End: 99, Label: "email", Score: 1}, {Start: 0, End: 3, Label: "unknown", Score: 1}, {Start: 0, End: 3, Label: "email", Score: float32(math.NaN())}} {
		if _, err := validateSpans(t.Context(), "秘密", []Span{span}); err == nil {
			t.Fatal("invalid inference output accepted")
		}
	}
	spans, err := validateSpans(t.Context(), "abc", []Span{{Start: 0, End: 1, Label: "account", Score: 0.79}, {Start: 1, End: 3, Label: "account", Score: 0.8}})
	if err != nil || len(spans) != 1 || spans[0].Start != 1 {
		t.Fatalf("confidence policy: %v %v", spans, err)
	}
}

func TestRuntimeUnavailableIsExplicit(t *testing.T) {
	if _, err := Open(t.TempDir(), t.TempDir(), "int8"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if err := Available(t.TempDir()); err == nil {
		t.Fatal("empty runtime directory reported available")
	}
}

type privacyCase struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Entities []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"entities"`
}

// Set both paths to opt into real model verification: the model directory
// is an installation written by privacy.Catalog and the runtime directory
// holds the ONNX Runtime library. A normal unit test run needs neither.
func TestAstrLinkGuardRealModel(t *testing.T) {
	modelDir, runtimeDir := os.Getenv("NEXTERM_PRIVACY_TEST_MODEL"), os.Getenv("NEXTERM_PRIVACY_TEST_RUNTIME")
	if modelDir == "" && runtimeDir == "" {
		t.Skip("real model and runtime paths not configured")
	}
	if modelDir == "" || runtimeDir == "" {
		t.Fatal("both model and runtime paths are required")
	}
	if err := Available(runtimeDir); err != nil {
		t.Fatal(err)
	}
	precision := os.Getenv("NEXTERM_PRIVACY_TEST_PRECISION")
	if precision == "" {
		precision = "int8"
	}
	otherPrecision := "fp32"
	if precision == otherPrecision {
		otherPrecision = "int8"
	}
	if mismatched, err := Open(modelDir, runtimeDir, otherPrecision); err == nil {
		mismatched.Close()
		t.Fatal("model loaded with mismatched precision")
	}
	engine, err := Open(modelDir, runtimeDir, precision)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	text := "请联系张伟，电话 13812345678，邮箱 zhangwei@example.com，收货地址：北京市海淀区中关村大街 27 号。"
	spans, err := engine.Detect(t.Context(), text)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"private_person": "张伟", "phone": "13812345678", "email": "zhangwei@example.com", "private_address": "北京市海淀区中关村大街 27 号"}
	for label, value := range want {
		if !hasSpan(text, spans, label, value) {
			t.Errorf("missing %s span; spans=%+v", label, spans)
		}
	}
	long := strings.Repeat("这是一条不包含个人信息的普通说明。", 90) + " Contact Jane Doe at jane.doe@example.org."
	spans, err = engine.Detect(t.Context(), long)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpan(long, spans, "email", "jane.doe@example.org") {
		t.Fatal("long input lost email after the first window")
	}
	if _, err := engine.Detect(t.Context(), strings.Repeat("a ", maxRequestTokens+1)); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("token limit: %v", err)
	}
	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if spans, err := engine.Detect(t.Context(), text); err != nil || !hasSpan(text, spans, "email", "zhangwei@example.com") {
				t.Errorf("concurrent detection: %v %+v", err, spans)
			}
		}()
	}
	wait.Wait()

	for _, file := range []string{"testdata/code-privacy-cases.json", "testdata/code-privacy-holdout.json"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var cases []privacyCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatal(err)
		}
		passed, expected, found, spurious := 0, 0, 0, 0
		for _, c := range cases {
			spans, err := engine.Detect(t.Context(), c.Text)
			if err != nil {
				t.Fatalf("%s: %v", c.ID, err)
			}
			ok := true
			for _, entity := range c.Entities {
				expected++
				if hasSpan(c.Text, spans, entity.Label, entity.Value) {
					found++
				} else {
					ok = false
				}
			}
			for _, span := range spans {
				if !expectedSpan(c, c.Text[span.Start:span.End], span.Label) {
					spurious++
					ok = false
				}
			}
			if ok {
				passed++
			} else {
				t.Logf("%s %s: spans %s", file, c.ID, describeSpans(c.Text, spans))
			}
		}
		t.Logf("%s: %d/%d cases exact, %d/%d entities found, %d unexpected spans", file, passed, len(cases), found, expected, spurious)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := engine.Detect(ctx, text); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled detection: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Detect(t.Context(), text); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed detection: %v", err)
	}
}

func hasSpan(text string, spans []Span, label, value string) bool {
	for _, span := range spans {
		if span.Label == label && text[span.Start:span.End] == value {
			return true
		}
	}
	return false
}

func expectedSpan(c privacyCase, value, label string) bool {
	for _, entity := range c.Entities {
		if entity.Label == label && entity.Value == value {
			return true
		}
	}
	return false
}

func describeSpans(text string, spans []Span) string {
	var parts []string
	for _, span := range spans {
		parts = append(parts, span.Label+"="+strings.TrimSpace(text[span.Start:span.End]))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
