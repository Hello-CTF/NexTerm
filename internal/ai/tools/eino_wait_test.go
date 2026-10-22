package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestEinoWaitForPatternThreeStates(t *testing.T) {
	execution := &Execution{
		JobID: "job", Scope: Scope{TabID: "tab"}, Permission: guard.Config{Mode: guard.ReadWrite}, Memory: guard.NewMemory(),
		Registry: NewRegistry(Dependencies{Terminal: &fakeTerminal{screen: Screen{Text: "READY", Tail: []string{"READY"}, IdleMS: 100}}}),
	}
	harness := &chatGrantHarness{execution: execution}
	for _, test := range []struct {
		name     string
		args     string
		wantOK   bool
		wantText string
	}{
		{name: "nonempty pattern", args: `{"pattern":"READY"}`, wantOK: true, wantText: "模式已出现"},
		{name: "explicit empty pattern", args: `{"pattern":""}`, wantOK: true, wantText: "模式已出现"},
		{name: "missing pattern without quiet", args: `{}`, wantOK: false, wantText: "pattern"},
		{name: "quiet only", args: `{"quiet_ms":50}`, wantOK: true, wantText: "安静"},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages, err := harness.invoke("wait", "wait_for", test.args)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 {
				t.Fatalf("messages = %+v", messages)
			}
			var result Output
			if err := json.Unmarshal([]byte(messages[0].Content), &result); err != nil {
				t.Fatal(err)
			}
			if result.OK != test.wantOK {
				t.Fatalf("result = %+v", result)
			}
			if test.wantText != "" && !strings.Contains(result.Text, test.wantText) {
				t.Fatalf("text = %q, want %q", result.Text, test.wantText)
			}
		})
	}
}

func TestEinoWaitForSchemaPatternIsOptionalString(t *testing.T) {
	execution := &Execution{Registry: NewRegistry(Dependencies{Terminal: &fakeTerminal{}}), Scope: Scope{TabID: "tab"}}
	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range available {
		info, err := candidate.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != "wait_for" {
			continue
		}
		converted, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(converted)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		pattern, found := schema.Properties["pattern"]
		if !found || pattern.Type != "string" {
			t.Fatalf("pattern schema = %+v", schema.Properties["pattern"])
		}
		for _, required := range schema.Required {
			if required == "pattern" {
				t.Fatalf("pattern must remain optional: %s", encoded)
			}
		}
		return
	}
	t.Fatal("wait_for schema not found")
}
