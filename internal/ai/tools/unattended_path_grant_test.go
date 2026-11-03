package tools

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// runWriteFile drives write_file through the tool graph the way a conversation turn does.
func runWriteFile(t *testing.T, ctx context.Context, files *fakeFS, permission guard.Config, memory *guard.Memory) {
	t.Helper()
	transport := &fakeTransport{files: files}
	execution := &Execution{
		JobID:      "job-cron",
		Scope:      Scope{SessionID: "s"},
		Permission: permission,
		Memory:     memory,
		Registry: NewRegistry(Dependencies{
			Transport: func(context.Context, string) (base.Transport, error) { return transport, nil },
		}),
	}
	available, err := execution.Tools()
	if err != nil {
		t.Fatal(err)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: available, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	message := schema.AssistantMessage("", []schema.ToolCall{{ID: "call-0", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"/var/log/app.log"}`}}, {ID: "call-1", Function: schema.FunctionCall{Name: "write_file", Arguments: `{"path":"/var/log/app.log","content":"x"}`}}})
	// Denial may surface as an error or as a failed result; the file system is the contract.
	_, _ = node.Invoke(ctx, message)
}

func TestUnattendedRunIgnoresSessionPathGrant(t *testing.T) {
	memory := guard.NewMemory()
	memory.AddPathRule("write_file", "/var/log/**")

	// Control: the same session grant lets an interactive read_write turn write.
	interactive := newFakeFS()
	runWriteFile(t, context.Background(), interactive, guard.Config{Mode: guard.ReadWrite}, memory)
	if _, ok := interactive.files["/var/log/app.log"]; !ok {
		t.Fatal("control: session path grant did not authorize the interactive write")
	}

	// A scheduled run shares the conversation memory but must stay unattended.
	scheduled := newFakeFS()
	runWriteFile(t, guard.WithUnattended(context.Background()), scheduled, guard.Config{Mode: guard.Unattended}, memory)
	if _, ok := scheduled.files["/var/log/app.log"]; ok {
		t.Fatal("unattended run wrote a file through a session path grant")
	}
}
