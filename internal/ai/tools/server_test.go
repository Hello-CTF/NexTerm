package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestSearchAndDockerArgumentsCannotReachShell(t *testing.T) {
	files := newFakeFS()
	registry, transport := fileRegistry(files)
	result := executeTool(registry, "j", "search_files", `{"path":"/tmp; touch /tmp/pwn","pattern":"*","by":"name"}`)
	if !result.OK {
		t.Fatal(result.Text)
	}
	if transport.calls() != 0 {
		t.Fatal("search used shell")
	}
	if len(files.listCalls) != 1 || files.listCalls[0] != "/tmp; touch /tmp/pwn" {
		t.Fatalf("path was not literal: %v", files.listCalls)
	}
	var dockerCalls atomic.Int64
	registry = NewRegistry(Dependencies{DockerLogs: func(context.Context, string, string, int, string) (string, error) {
		dockerCalls.Add(1)
		return "", nil
	}})
	result = registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, Call{ID: "d", Name: "docker_logs", Args: json.RawMessage(`{"container_id":"web; touch /tmp/pwn"}`)}, nil)
	if result.OK || dockerCalls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d", result, dockerCalls.Load())
	}
}

func TestWaitForCancellation(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "working", Tail: []string{"working"}}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan Output, 1)
	go func() {
		finished <- registry.Execute(ctx, "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"done","timeout_ms":120000}`)}, nil)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case result := <-finished:
		if result.OK {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("wait_for ignored cancellation")
	}
}

func TestInvalidRegexReturnsImmediately(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{}})
	result := registry.Execute(context.Background(), "j", Scope{TabID: "t"}, Call{ID: "w", Name: "wait_for", Args: json.RawMessage(`{"pattern":"[","timeout_ms":120000}`)}, nil)
	if result.OK || !strings.Contains(result.Text, "正则无效") {
		t.Fatalf("result=%+v", result)
	}
}

func TestCrossScopeTabRejected(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{}, TabSession: func(string) string { return "other" }})
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if result.OK || !strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecCancellation(t *testing.T) {
	transport := &fakeTransport{files: newFakeFS(), block: true}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan Output, 1)
	go func() {
		finished <- registry.Execute(ctx, "j", Scope{SessionID: "s"}, Call{ID: "e", Name: "exec_commands", Args: json.RawMessage(`{"commands":["sleep 100"]}`)}, nil)
	}()
	for transport.calls() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case result := <-finished:
		if result.OK {
			t.Fatal("cancelled exec succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("exec ignored cancellation")
	}
}

func TestAuditRedactionAndDBTruncation(t *testing.T) {
	var audits []AuditEntry
	transport := &fakeTransport{files: newFakeFS(), result: base.ExecResult{Stdout: "ok"}}
	registry := NewRegistry(Dependencies{
		Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		Audit:     func(_ context.Context, entry AuditEntry) error { audits = append(audits, entry); return nil },
	})
	result := executeTool(registry, "j", "exec_commands", `{"commands":["curl --password secret https://example.test"]}`)
	if !result.OK || len(audits) != 1 || strings.Contains(fmt.Sprint(audits[0].Payload), "secret") {
		t.Fatalf("result=%+v audits=%+v", result, audits)
	}

	rows := make([][]any, 51)
	for i := range rows {
		rows[i] = []any{i}
	}
	registry = NewRegistry(Dependencies{Database: &fakeDatabase{result: db.QueryResult{Columns: []string{"n"}, Rows: rows}}})
	result = registry.Execute(context.Background(), "j", Scope{ConnID: "c"}, Call{ID: "q", Name: "db_query", Args: json.RawMessage(`{"sql":"SELECT n FROM t"}`)}, nil)
	if !result.OK || !result.Truncated || strings.Count(result.Text, "\n") < 50 {
		t.Fatalf("result=%+v", result)
	}
}
