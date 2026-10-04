package guard

import (
	"encoding/json"
	"testing"
)

func TestDangerRulesMatchArgumentValuesNotJSONSyntax(t *testing.T) {
	t.Parallel()
	config := Config{Mode: ReadWrite, DangerRules: []string{"path", "content", "commands"}}
	if got := ClassifyTool("write_file", json.RawMessage(`{"path":"/tmp/notes.txt","content":"hello"}`), config); got.Risk != NeedsConfirm {
		t.Fatalf("key-name match false positive: %+v", got)
	}
	if got := ClassifyTool("exec_commands", json.RawMessage(`{"commands":["ls"]}`), config); got.Risk != Safe {
		t.Fatalf("key-name match on read-only tool: %+v", got)
	}
	if got := ClassifyTool("write_file", json.RawMessage(`{"path":null}`), config); got.Risk != NeedsConfirm {
		t.Fatalf("null value false positive: %+v", got)
	}
	if got := ClassifyTool("write_file", json.RawMessage(`{"path":""}`), config); got.Risk != NeedsConfirm {
		t.Fatalf("empty value false positive: %+v", got)
	}
	if got := ClassifyTool("read_file", json.RawMessage(`{"path":"/srv/app/data.json"}`), config); got.Risk != Safe {
		t.Fatalf("json punctuation false positive: %+v", got)
	}
}

func TestDangerRulesStillMatchArgumentValues(t *testing.T) {
	t.Parallel()
	config := Config{Mode: ReadWrite, DangerRules: []string{"prod-secret"}}
	if got := ClassifyTool("read_file", json.RawMessage(`{"path":"/srv/prod-secret/db"}`), config); got.Risk != Danger {
		t.Fatalf("value match missed: %+v", got)
	}
	if got := ClassifyTool("write_file", json.RawMessage(`{"path":"/tmp/x","content":"deploy prod-secret now"}`), config); got.Risk != Danger {
		t.Fatalf("nested value match missed: %+v", got)
	}
	if got := ClassifyTool("docker_logs", json.RawMessage(`{"container_id":"prod-secret"}`), config); got.Risk != Danger {
		t.Fatalf("fixed tool value match missed: %+v", got)
	}
	numberRule := Config{Mode: ReadWrite, DangerRules: []string{"3306"}}
	if got := ClassifyTool("db_query", json.RawMessage(`{"sql":"SELECT 1","port":3306}`), numberRule); got.Risk != Danger {
		t.Fatalf("numeric value match missed: %+v", got)
	}
	if got := ClassifyTool("docker_exec", json.RawMessage(`{"container_id":"c","cmd":"rm -rf / # prod-secret"}`), config); got.Risk != Forbidden {
		t.Fatalf("builtin forbidden downgraded by custom rule: %+v", got)
	}
	if got := ClassifyTool("write_file", json.RawMessage(`{"path":"/tmp/prod-secret.txt"`), config); got.Risk != Danger {
		t.Fatalf("unparseable args must fall back to raw matching: %+v", got)
	}
}

func TestDangerRulesSkipInteractionTools(t *testing.T) {
	t.Parallel()
	config := Config{Mode: ReadWrite, DangerRules: []string{"prod-secret"}}
	if got := ClassifyTool("ask_user", json.RawMessage(`{"question":"about prod-secret"}`), config); got.Risk != Safe {
		t.Fatalf("ask_user should stay exempt: %+v", got)
	}
	if got := ClassifyTool("todo_write", json.RawMessage(`{"todos":["prod-secret"]}`), config); got.Risk != Safe {
		t.Fatalf("todo_write should stay exempt: %+v", got)
	}
}
