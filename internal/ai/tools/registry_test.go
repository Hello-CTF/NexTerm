package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryToolsDispatchable(t *testing.T) {
	t.Parallel()
	schemas := toolSchemas()
	seen := map[string]bool{}
	registry := NewRegistry(Dependencies{})
	for _, schema := range schemas {
		if seen[schema.Name] {
			t.Fatalf("duplicate %s", schema.Name)
		}
		seen[schema.Name] = true
		if err := registry.Validate(Call{Name: schema.Name}); err != nil && strings.Contains(err.Error(), "未知工具") {
			t.Errorf("%s not dispatchable", schema.Name)
		}
	}
	if !PlanAllowed("redis_scan") || !PlanAllowed(NotifyTool) || PlanAllowed("exec_commands") || PlanAllowed("write_file") || PlanAllowed(ReminderTool) {
		t.Fatal("incorrect plan policy")
	}
}

func TestSchemaValidationBeforeConfirmation(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Dependencies{})
	for _, test := range []struct {
		args  string
		valid bool
	}{
		{`{"path":"/a","content":"x"}`, true},
		{`{"path":"/a"}`, false},
		{`{"path":1,"content":"x"}`, false},
		{`{"path":"/a","content":"x","unexpected":true}`, false},
		{`null`, false},
		{`{broken`, false},
		{`{"path":"/a","content":"x"} {}`, false},
	} {
		err := registry.Validate(Call{Name: "write_file", Args: json.RawMessage(test.args)})
		if (err == nil) != test.valid {
			t.Errorf("%s: err=%v", test.args, err)
		}
	}
	if err := registry.Validate(Call{Name: "docker_control", Args: json.RawMessage(`{"container_id":"web","action":"kill"}`)}); err == nil {
		t.Fatal("invalid enum reached confirmation")
	}
	if err := registry.Validate(Call{Name: "wait_for", Args: json.RawMessage(`{"pattern":"x","timeout_ms":1.5}`)}); err == nil {
		t.Fatal("fractional integer reached confirmation")
	}
}

func TestEncodeSemanticKeys(t *testing.T) {
	t.Parallel()
	encoded, err := EncodeKeys("ls<enter><ctrl+c><up><lt>", true)
	if err != nil {
		t.Fatal(err)
	}
	want := "ls\r\x03\x1b[A<\r"
	if string(encoded) != want {
		t.Fatalf("encoded=%q want=%q", encoded, want)
	}
	encoded, err = EncodeKeys("<unknown>", false)
	if err != nil || string(encoded) != "<unknown>" {
		t.Fatalf("unknown tag should stay literal: %q err=%v", encoded, err)
	}
}

func TestNotifyDeliversThroughDependency(t *testing.T) {
	t.Parallel()
	var delivered []string
	registry := NewRegistry(Dependencies{Notify: func(_ context.Context, text string) error {
		delivered = append(delivered, text)
		return nil
	}})
	result := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "c1", Name: NotifyTool, Args: json.RawMessage(`{"message":" 构建完成 "}`)}, nil)
	if !result.OK {
		t.Fatalf("notify failed: %s", result.Text)
	}
	if len(delivered) != 1 || delivered[0] != "构建完成" {
		t.Fatalf("delivered=%v", delivered)
	}
	if !strings.Contains(result.Text, "写进当轮回复") {
		t.Fatalf("result must remind the model to repeat the notice in the reply: %q", result.Text)
	}
}

func TestNotifyValidationAndMissingService(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Dependencies{})
	if result := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "c1", Name: NotifyTool, Args: json.RawMessage(`{"message":"x"}`)}, nil); result.OK || !strings.Contains(result.Text, "通知服务未配置") {
		t.Fatalf("nil dependency: %+v", result)
	}
	registry = NewRegistry(Dependencies{Notify: func(context.Context, string) error { return nil }})
	for name, args := range map[string]string{
		"blank":    `{"message":"  "}`,
		"overlong": `{"message":"` + strings.Repeat("长", notifyMessageMax+1) + `"}`,
	} {
		result := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "c1", Name: NotifyTool, Args: json.RawMessage(args)}, nil)
		if result.OK {
			t.Fatalf("%s message must be rejected", name)
		}
	}
}
