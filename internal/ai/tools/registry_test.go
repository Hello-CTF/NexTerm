package tools

import (
	"encoding/json"
	"testing"
)

func TestRegistryExactly22Tools(t *testing.T) {
	t.Parallel()
	names := Names()
	if len(names) != 22 {
		t.Fatalf("tool count=%d: %v", len(names), names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate %s", name)
		}
		seen[name] = true
	}
	registry := NewRegistry(Dependencies{})
	for _, name := range names {
		if !registry.Known(name) {
			t.Errorf("%s not dispatchable", name)
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
