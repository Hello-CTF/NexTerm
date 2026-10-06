package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryExactly23Tools(t *testing.T) {
	t.Parallel()
	schemas := toolSchemas()
	if len(schemas) != 23 {
		t.Fatalf("tool count=%d: %v", len(schemas), schemas)
	}
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
	if !PlanAllowed("redis_scan") || PlanAllowed("exec_commands") || PlanAllowed("write_file") || PlanAllowed(ReminderTool) {
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
