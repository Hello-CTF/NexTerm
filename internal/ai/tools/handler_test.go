package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/schema"
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

func TestDatabaseQueryModelTextOnlyTranslatesGeneratedFooter(t *testing.T) {
	rows := make([][]any, 51)
	for index := range rows {
		rows[index] = []any{"仅显示前 7 行（查询返回 7 行）"}
	}
	registry := NewRegistry(Dependencies{Database: &fakeDatabase{result: db.QueryResult{Columns: []string{"alias"}, Rows: rows}}})
	result := registry.Execute(context.Background(), "job", Scope{ConnID: "conn"}, Call{ID: "query", Name: "db_query", Args: json.RawMessage(`{"sql":"SELECT alias FROM t"}`)}, nil)
	if !result.OK || !result.Truncated {
		t.Fatalf("query = %+v", result)
	}
	if strings.Count(result.Text, "仅显示前 7 行（查询返回 7 行）") != 50 || !strings.Contains(result.Text, "仅显示前 50 行（查询返回 51 行）") {
		t.Fatalf("Chinese query rows or footer changed: %q", result.Text)
	}
	if strings.Count(result.ModelText, "仅显示前 7 行（查询返回 7 行）") != 50 || !strings.Contains(result.ModelText, "showing only the first 50 rows (query returned 51 rows)") {
		t.Fatalf("SQL aliases were rewritten or real footer was not projected: %q", result.ModelText)
	}
}

func TestDatabaseQueryDriverErrorModelTextStaysOpaque(t *testing.T) {
	driverDetail := "没有资产"
	registry := NewRegistry(Dependencies{Database: &fakeDatabase{result: db.QueryResult{Error: &driverDetail}}})
	result := registry.Execute(context.Background(), "job", Scope{ConnID: "conn"}, Call{ID: "query", Name: "db_query", Args: json.RawMessage(`{"sql":"SELECT broken"}`)}, nil)
	if result.OK || result.ExitCode != 1 || result.Text != driverDetail || result.ModelText != driverDetail {
		t.Fatalf("query = %+v, want opaque driver detail in both text channels", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	message := schema.ToolMessage(string(encoded), "query")
	message.ToolName = "db_query"
	projected := provider.ProjectModelMessages([]*schema.Message{message})
	var envelope struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(projected[0].Content), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Text != driverDetail {
		t.Fatalf("model text = %q, want original driver detail %q", envelope.Text, driverDetail)
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

func TestExecModelTextPreservesMarkerLikeUserData(t *testing.T) {
	transport := &fakeTransport{result: base.ExecResult{Stdout: "command [执行失败: user data]\nstdout [输出已截断]"}}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "session"}, execCall("call", "echo '[执行失败: command data]'"), nil)
	if !result.OK || result.ModelText != result.Text {
		t.Fatalf("marker-like user data changed: %+v", result)
	}
	if _, err := completionFromOutput(result); err != nil {
		t.Fatalf("marker-like user data changed the outcome: %v", err)
	}
}

func TestExecModelTextUsesKnownFailureAndTruncation(t *testing.T) {
	t.Run("known failure", func(t *testing.T) {
		transport := &fakeTransport{result: base.ExecResult{Stdout: "stdout"}, execErr: fmt.Errorf("backend broken")}
		registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "session"}, execCall("call", "echo hi"), nil)
		if !result.OK || !result.ExecFailed {
			t.Fatalf("structured exec state = %+v", result)
		}
		if !strings.Contains(result.Text, "[执行失败: backend broken]") || !strings.Contains(result.ModelText, "[execution failed: backend broken]") {
			t.Fatalf("known failure was not projected from execErr: %+v", result)
		}
		if _, err := completionFromOutput(result); err == nil {
			t.Fatal("known transport failure was not surfaced to outcome recording")
		}
	})
	t.Run("known truncation", func(t *testing.T) {
		transport := &fakeTransport{result: base.ExecResult{Stdout: "stdout [输出已截断]\n" + strings.Repeat("x", MaxOutputBytes)}}
		registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
		result := registry.Execute(context.Background(), "job", Scope{SessionID: "session"}, execCall("call", "echo hi"), nil)
		if !result.OK || !result.Truncated || !strings.HasSuffix(result.Text, "\n[输出已截断]") || !strings.HasSuffix(result.ModelText, "\n[output truncated]") {
			t.Fatalf("known truncation was not projected from cap state: %+v", result)
		}
		if !strings.Contains(result.ModelText, "stdout [输出已截断]") {
			t.Fatalf("marker-like stdout was rewritten: %q", result.ModelText)
		}
	})
}

func TestModelTextOnlyMarksGeneratedEmptyResults(t *testing.T) {
	emptySearch, _ := fileRegistry(newFakeFS())
	literalSearchFiles := newFakeFS()
	literalSearchFiles.dirs["/src"] = []base.FileEntry{{Name: "literal", Path: "无匹配", Kind: base.FileFile}}
	literalSearch, _ := fileRegistry(literalSearchFiles)
	tests := []struct {
		name          string
		registry      *Registry
		scope         Scope
		tool          string
		args          string
		wantText      string
		wantModelText string
	}{
		{name: "empty tables", registry: NewRegistry(Dependencies{Database: &fakeDatabase{tables: []string{}}}), scope: Scope{ConnID: "c"}, tool: "db_list_tables", args: `{}`, wantText: "无表", wantModelText: "no tables"},
		{name: "literal table name", registry: NewRegistry(Dependencies{Database: &fakeDatabase{tables: []string{"无表"}}}), scope: Scope{ConnID: "c"}, tool: "db_list_tables", args: `{}`, wantText: "无表"},
		{name: "empty search", registry: emptySearch, scope: Scope{SessionID: "s"}, tool: "search_files", args: `{"path":"/src","pattern":"*","by":"name"}`, wantText: "无匹配", wantModelText: "no matches"},
		{name: "literal search result", registry: literalSearch, scope: Scope{SessionID: "s"}, tool: "search_files", args: `{"path":"/src","pattern":"*","by":"name"}`, wantText: "无匹配"},
		{name: "empty docker logs", registry: NewRegistry(Dependencies{DockerLogs: func(context.Context, string, string, int, string) (string, error) { return "", nil }}), scope: Scope{SessionID: "s"}, tool: "docker_logs", args: `{"container_id":"web"}`, wantText: "日志为空", wantModelText: "no logs"},
		{name: "literal docker logs", registry: NewRegistry(Dependencies{DockerLogs: func(context.Context, string, string, int, string) (string, error) { return "日志为空", nil }}), scope: Scope{SessionID: "s"}, tool: "docker_logs", args: `{"container_id":"web"}`, wantText: "日志为空"},
		{name: "empty docker exec", registry: NewRegistry(Dependencies{DockerExec: func(context.Context, string, string, string) (ExecResult, error) { return ExecResult{}, nil }}), scope: Scope{SessionID: "s"}, tool: "docker_exec", args: `{"container_id":"web","cmd":"true"}`, wantText: "(无输出)", wantModelText: "(no output)"},
		{name: "literal docker exec", registry: NewRegistry(Dependencies{DockerExec: func(context.Context, string, string, string) (ExecResult, error) {
			return ExecResult{Output: "(无输出)"}, nil
		}}), scope: Scope{SessionID: "s"}, tool: "docker_exec", args: `{"container_id":"web","cmd":"true"}`, wantText: "(无输出)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.registry.Execute(context.Background(), "j", test.scope, Call{ID: "call", Name: test.tool, Args: json.RawMessage(test.args)}, nil)
			if !result.OK || result.Text != test.wantText || result.ModelText != test.wantModelText {
				t.Fatalf("result = %+v, want text %q and modelText %q", result, test.wantText, test.wantModelText)
			}
		})
	}
}
