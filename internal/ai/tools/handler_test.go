package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestDockerHandlers(t *testing.T) {
	var audits []AuditEntry
	deps := Dependencies{
		DockerPS: func(context.Context, string) ([]Container, error) {
			return []Container{{Name: "web", State: "running", Image: "nginx"}}, nil
		},
		DockerLogs: func(_ context.Context, _, container string, tail int, grep string) (string, error) {
			if container != "web" || tail != 20 || grep != "ok" {
				return "", fmt.Errorf("wrong logs args")
			}
			return "ok line", nil
		},
		DockerExec: func(context.Context, string, string, string) (ExecResult, error) {
			return ExecResult{Output: "failed", ExitCode: 2}, nil
		},
		DockerAct: func(_ context.Context, _, container, action string) error {
			if container != "web" || action != "restart" {
				return fmt.Errorf("wrong action")
			}
			return nil
		},
		Audit: func(_ context.Context, entry AuditEntry) error { audits = append(audits, entry); return nil },
	}
	registry := NewRegistry(deps)
	for _, test := range []struct{ name, args, want string }{
		{"docker_ps", `{}`, "web [running] nginx"},
		{"docker_logs", `{"container_id":"web","tail":20,"grep":"ok"}`, "ok line"},
		{"docker_control", `{"container_id":"web","action":"restart"}`, "restart web 完成"},
	} {
		result := executeTool(registry, "j", test.name, test.args)
		if !result.OK || !strings.Contains(result.Text, test.want) {
			t.Errorf("%s result=%+v", test.name, result)
		}
	}
	result := executeTool(registry, "j", "docker_exec", `{"container_id":"web","cmd":"false"}`)
	if result.OK || result.ExitCode != 2 {
		t.Fatalf("nonzero docker exec=%+v", result)
	}
	if len(audits) != 5 {
		t.Fatalf("generic and specialized audits=%d", len(audits))
	}
}

func TestDatabaseAndMetaHandlers(t *testing.T) {
	registry := NewRegistry(Dependencies{
		Database: &fakeDatabase{result: db.QueryResult{Columns: []string{"ok"}, Rows: [][]any{{true}}}},
		ListAssets: func(context.Context) ([]Asset, error) {
			return []Asset{{Name: "web", Kind: "ssh", Host: "host:22", Username: "deploy"}}, nil
		},
	})
	for _, test := range []struct {
		name, args, want string
	}{
		{"db_list_tables", `{}`, "users"},
		{"db_describe", `{"table":"users"}`, "columns"},
		{"db_query", `{"sql":"SELECT 1"}`, "ok"},
		{"redis_scan", `{}`, "cursor=7"},
		{"list_assets", `{}`, "web [ssh] host:22 deploy"},
	} {
		result := registry.Execute(context.Background(), "j", Scope{ConnID: "c"}, Call{ID: test.name, Name: test.name, Args: json.RawMessage(test.args)}, nil)
		if !result.OK || !strings.Contains(result.Text, test.want) {
			t.Errorf("%s result=%+v", test.name, result)
		}
	}
	question := executeTool(registry, "j", "ask_user", `{"question":"继续？","options":["是","否"]}`)
	if question.Question == nil || question.Question.Question != "继续？" {
		t.Fatalf("question=%+v", question)
	}
	todos := executeTool(registry, "j", "todo_write", `{"todos":[{"content":"step","status":"in_progress"}]}`)
	if !todos.OK || len(todos.Todos) != 1 || !strings.Contains(todos.Text, "[~] step") {
		t.Fatalf("todos=%+v", todos)
	}
	emptyTodos := executeTool(registry, "j", "todo_write", `{"todos":[]}`)
	if !emptyTodos.OK || emptyTodos.Todos == nil || len(emptyTodos.Todos) != 0 {
		t.Fatalf("empty todos must emit a clearing event: %+v", emptyTodos)
	}
	plan := executeTool(registry, "j", "exit_plan_mode", `{"plan":"step one"}`)
	if !plan.OK || plan.Plan != "step one" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestTerminalAndDirectoryHandlers(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "visible", CursorRow: 2, CursorCol: 3, IdleMS: 9}}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	scope := Scope{SessionID: "s", TabID: "t"}
	result := registry.Execute(context.Background(), "j", scope, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if !result.OK || !strings.Contains(result.Text, "光标 (2, 3)") {
		t.Fatalf("screen=%+v", result)
	}
	result = registry.Execute(context.Background(), "j", scope, Call{ID: "s", Name: "send_keys", Args: json.RawMessage(`{"keys":"ls","enter":true}`)}, nil)
	if !result.OK || len(terminal.writes) != 1 || string(terminal.writes[0]) != "ls\r" {
		t.Fatalf("send=%+v writes=%q", result, terminal.writes)
	}

	files := newFakeFS()
	entries := make([]base.FileEntry, 501)
	for i := range entries {
		entries[i] = base.FileEntry{Name: fmt.Sprintf("%03d", i), Kind: base.FileFile}
	}
	files.dirs["/many"] = entries
	registry, _ = fileRegistry(files)
	result = executeTool(registry, "j", "list_dir", `{"path":"/many"}`)
	if !result.OK || !result.Truncated || len(strings.Split(strings.TrimSpace(result.Text), "\n")) != 500 {
		t.Fatalf("list result=%+v", result)
	}
}

func TestSearchContentAndExecOutput(t *testing.T) {
	files := newFakeFS()
	files.dirs["/src"] = []base.FileEntry{{Name: "a.go", Path: "/src/a.go", Kind: base.FileFile}}
	files.files["/src/a.go"] = []byte("hello\nworld hello\n")
	registry, transport := fileRegistry(files)
	result := executeTool(registry, "j", "search_files", `{"path":"/src","pattern":"hello","by":"content"}`)
	if !result.OK || !strings.Contains(result.Text, "/src/a.go:1:hello") || !strings.Contains(result.Text, ":2:world hello") {
		t.Fatalf("search=%+v", result)
	}
	if transport.calls() != 0 {
		t.Fatal("content search used shell")
	}
	code := 3
	transport.result = base.ExecResult{Stdout: "out", Stderr: "err", ExitCode: &code}
	result = executeTool(registry, "j", "exec_commands", `{"commands":["false"]}`)
	if !result.OK || result.ExitCode != 3 || !strings.Contains(result.Text, "[stderr]") || !strings.Contains(result.Text, "[exit_code=3]") {
		t.Fatalf("exec=%+v", result)
	}
}
