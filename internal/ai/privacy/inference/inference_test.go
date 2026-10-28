package inference

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
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
}

// Set both paths to opt into real model verification. Release/CI verification
// must provide them; a normal unit test run needs no downloaded native assets.
func TestAstrLinkGuardRealModel(t *testing.T) {
	modelDir, runtimeDir := os.Getenv("NEXTERM_PRIVACY_TEST_MODEL"), os.Getenv("NEXTERM_PRIVACY_TEST_RUNTIME")
	if modelDir == "" && runtimeDir == "" {
		t.Skip("real model and runtime paths not configured")
	}
	if modelDir == "" || runtimeDir == "" {
		t.Fatal("both model and runtime paths are required")
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
		found := false
		for _, span := range spans {
			if span.Label == label && text[span.Start:span.End] == value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %s span; spans=%+v", label, spans)
		}
	}
	long := strings.Repeat("这是一条不包含个人信息的普通说明。", 90) + " Contact Jane Doe at jane.doe@example.org."
	spans, err = engine.Detect(t.Context(), long)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, span := range spans {
		if span.Label == "email" && long[span.Start:span.End] == "jane.doe@example.org" {
			found = true
		}
	}
	if !found {
		t.Fatal("long input lost email after the first window")
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
