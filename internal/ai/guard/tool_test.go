package guard

import (
	"encoding/json"
	"testing"
)

func classifyToolForTest(name, args string) Ruling {
	return ClassifyTool(name, json.RawMessage(args), Config{Mode: ReadWrite})
}

func TestSendKeysUsesActualSubmitBehavior(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args string
		risk Risk
	}{
		{`{"keys":"sudo reboot<enter>","enter":false}`, Danger},
		{`{"keys":"rm -rf -- /\n","enter":false}`, Forbidden},
		{`{"keys":"systemctl restart x<ctrl+m>"}`, NeedsConfirm},
		{`{"keys":"","enter":true}`, NeedsConfirm},
		{`{"keys":"y","enter":false}`, Safe},
		{`{"keys":"<ctrl+c>","enter":false}`, NeedsConfirm},
		{`{"keys":"ls","enter":true}`, Safe},
	}
	for _, test := range tests {
		if got := classifyToolForTest("send_keys", test.args); got.Risk != test.risk {
			t.Errorf("%s: got %v, want %s", test.args, got, test.risk)
		}
	}
}

func TestRegisteredToolGuardCoverage(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read_file", "list_dir", "search_files", "read_screen", "wait_for", "list_assets", "docker_ps", "docker_logs", "db_list_tables", "db_describe", "redis_scan", "ask_user", "todo_write", "exit_plan_mode"} {
		if got := classifyToolForTest(name, `{}`); got.Risk != Safe {
			t.Errorf("%s: got %v", name, got)
		}
	}
	if got := classifyToolForTest("docker_exec", `{"container_id":"c","cmd":"rm -rf /"}`); got.Risk != Forbidden {
		t.Fatalf("nested Docker command: %v", got)
	}
	if got := classifyToolForTest("db_query", `{"sql":"SELECT 1; DROP DATABASE x"}`); got.Risk != Forbidden {
		t.Fatalf("multi-statement SQL: %v", got)
	}
}

func TestCustomDangerRuleCoversFixedToolsWithoutDowngradingForbidden(t *testing.T) {
	t.Parallel()
	config := Config{Mode: Silent, DangerRules: []string{"prod-secret"}}
	if got := ClassifyTool("docker_logs", json.RawMessage(`{"container_id":"prod-secret"}`), config); got.Risk != Danger {
		t.Fatalf("fixed tool custom danger: %v", got)
	}
	if got := ClassifyTool("docker_exec", json.RawMessage(`{"container_id":"c","cmd":"rm -rf / # prod-secret"}`), config); got.Risk != Forbidden {
		t.Fatalf("custom danger downgraded forbidden: %v", got)
	}
}
