package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestGuardedConvertsPanicIntoFailureOutput(t *testing.T) {
	t.Parallel()
	output, err := Guarded(context.Background(), "list_assets", func() (Output, error) {
		panic("connection exploded")
	})
	if err != nil {
		t.Fatalf("panic leaked as error: %v", err)
	}
	if output.OK {
		t.Fatal("panic became a success output")
	}
	if !output.Panic {
		t.Fatal("panic output is not marked as a structured panic failure")
	}
	if !strings.Contains(output.Text, "list_assets") || !strings.Contains(output.Text, "connection exploded") {
		t.Fatalf("failure output lost panic context: %+v", output)
	}
	if output.ExitCode == 0 {
		t.Fatal("failure output must carry a non-zero exit code")
	}
}

func TestGuardedKeepsOrdinaryResults(t *testing.T) {
	t.Parallel()
	output, err := Guarded(context.Background(), "read_file", func() (Output, error) {
		return OK("fine"), nil
	})
	if err != nil || !output.OK || output.Text != "fine" {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	if output.Panic {
		t.Fatal("ordinary success marked as panic")
	}
}

func TestGuardedDoesNotSwallowCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := Guarded(ctx, "wait_for", func() (Output, error) {
		<-ctx.Done()
		panic("late panic")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation swallowed into tool output: output=%+v err=%v", output, err)
	}
	if output.OK || output.Text != "" {
		t.Fatalf("canceled call produced output: %+v", output)
	}
}

func TestPanicDiagnosticsAreStackFreeAndSafe(t *testing.T) {
	t.Parallel()
	output := panicOutput("db_query", "dial tcp 10.0.0.1: password=hunter2\napi_key=abcd1234\r\n\x00\x1b[31mstack frame leaking internals")
	if !output.Panic || output.OK {
		t.Fatalf("output = %+v", output)
	}
	if strings.Contains(output.Text, "hunter2") || strings.Contains(output.Text, "abcd1234") {
		t.Fatalf("panic diagnostic leaked a secret: %q", output.Text)
	}
	if strings.ContainsAny(output.Text, "\n\r\x00\x1b") {
		t.Fatalf("panic diagnostic is not single-line printable: %q", output.Text)
	}
	if strings.Contains(output.Text, "goroutine") || strings.Contains(output.Text, ".go:") {
		t.Fatalf("panic diagnostic looks like a stack trace: %q", output.Text)
	}
}

func TestPanicDiagnosticsCapNonErrorValues(t *testing.T) {
	t.Parallel()
	output := panicOutput("exec_commands", 42)
	if !strings.Contains(output.Text, "42") {
		t.Fatalf("non-error panic value lost: %q", output.Text)
	}
	long := panicOutput("read_file", strings.Repeat("长", panicDiagnosticMaxRunes*2))
	if got := len([]rune(long.Text)); got > len([]rune("工具 read_file 执行时崩溃："))+panicDiagnosticMaxRunes+1 {
		t.Fatalf("panic diagnostic not capped: %d runes", got)
	}
	empty := panicOutput("read_file", nil)
	if !strings.Contains(empty.Text, "未知崩溃") {
		t.Fatalf("nil panic value produced empty diagnostic: %q", empty.Text)
	}
}

func TestRegistryExecuteIsolatesDependencyPanic(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Dependencies{ListAssets: func(context.Context) ([]Asset, error) {
		panic("资产后端崩溃")
	}})
	output := registry.Execute(context.Background(), "job", Scope{}, Call{ID: "c1", Name: "list_assets", Args: json.RawMessage(`{}`)}, nil)
	if output.OK || !output.Panic {
		t.Fatalf("registry leaked a panic as plain output: %+v", output)
	}
	if !strings.Contains(output.Text, "资产后端崩溃") {
		t.Fatalf("registry panic output lost diagnostics: %+v", output)
	}
}

func TestSpawnToolPanicBecomesStructuredFailure(t *testing.T) {
	t.Parallel()
	spawn := spawnOutputTool{InvokableTool: panickingTool{}}
	output, err := spawn.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("panic leaked as error: %v", err)
	}
	if !strings.Contains(output, `"panic":true`) || !strings.Contains(output, "子代理崩溃") {
		t.Fatalf("spawn panic output not structured: %s", output)
	}
}

type panickingTool struct{}

func (panickingTool) Info(context.Context) (info *schema.ToolInfo, err error) {
	return &schema.ToolInfo{Name: "spawn_agent"}, nil
}

func (panickingTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	panic("子代理崩溃")
}
