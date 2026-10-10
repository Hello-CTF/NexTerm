package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/conditional"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestR1IndeterminateWriteIsReconciledOnceWithoutRetry(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(map[bool]string{true: "content-confirmed", false: "content-differs"}[committed], func(t *testing.T) {
			files := newFakeFS()
			files.files["/a"] = []byte("old")
			registry, _ := fileRegistry(files)
			executeTool(registry, "j", "read_file", `{"path":"/a"}`)
			write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
			preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
			if err != nil {
				t.Fatal(err)
			}
			files.writeErr = &conditional.IndeterminateError{
				Expected: conditional.VersionOf([]byte("old")).Expectation(),
				New:      conditional.VersionOf([]byte("new")),
				Cause:    errors.New("response lost"),
			}
			files.writeErrAfterCommit = committed
			result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
			if files.writeCalls != 1 {
				t.Fatalf("write was retried %d times", files.writeCalls)
			}
			if committed {
				if !result.OK || !strings.Contains(result.Text, "原始写入结果不确定") || !strings.Contains(result.Text, "未重试") {
					t.Fatalf("indeterminate confirmed result = %+v", result)
				}
			} else if result.OK || !strings.Contains(result.Text, "未重试") {
				t.Fatalf("indeterminate differing result = %+v", result)
			}
		})
	}
}

func TestR1WriteDescriptionsDiscloseBothSemantics(t *testing.T) {
	if !strings.Contains(einoDescriptions["write_file"], "以原子方式创建新文件") || !strings.Contains(einoDescriptions["write_file"], "覆盖现有文件需用户授权") {
		t.Fatalf("write_file description = %q", einoDescriptions["write_file"])
	}
	if !strings.Contains(einoDescriptions["edit_file"], "非事务覆盖") {
		t.Fatalf("edit_file description = %q", einoDescriptions["edit_file"])
	}
}

func TestR4CanceledIndeterminateWriteStillReconcilesAndAudits(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	transport := &fakeTransport{files: files}
	var audits []AuditEntry
	resolver := func(ctx context.Context, _ string) (base.Transport, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return transport, nil
	}
	registry := NewRegistry(Dependencies{Transport: resolver, Audit: func(_ context.Context, entry AuditEntry) error {
		audits = append(audits, entry)
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	preparation, err := registry.Prepare(ctx, "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	files.writeErr = &conditional.IndeterminateError{Expected: conditional.VersionOf([]byte("old")).Expectation(), New: conditional.VersionOf([]byte("new")), Cause: errors.New("response lost")}
	files.writeErrAfterCommit = true
	files.writeHook = cancel
	result := registry.Execute(ctx, "j", Scope{SessionID: "s"}, write, preparation)
	if !result.OK || !strings.Contains(result.Text, "原始写入结果不确定") || files.writeCalls != 1 {
		t.Fatalf("canceled reconciliation result = %+v, calls = %d", result, files.writeCalls)
	}
	verified := false
	for _, entry := range audits {
		if entry.Kind != "write_file" {
			continue
		}
		payload, ok := entry.Payload.(map[string]any)
		verified = ok && payload["writeSemantics"] == writeReplaceSemantics && payload["outcome"] == "indeterminate_content_confirmed"
	}
	if !verified {
		t.Fatalf("missing indeterminate audit outcome: %+v", audits)
	}
}

func TestR4IndeterminateResolveFailurePreservesOriginalError(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	transport := &fakeTransport{files: files}
	calls := 0
	resolver := func(context.Context, string) (base.Transport, error) {
		calls++
		if calls == 4 {
			return nil, errors.New("resolve failed")
		}
		return transport, nil
	}
	registry := NewRegistry(Dependencies{Transport: resolver})
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	files.writeErr = &conditional.IndeterminateError{Expected: conditional.VersionOf([]byte("old")).Expectation(), New: conditional.VersionOf([]byte("new")), Cause: errors.New("response lost")}
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
	if result.OK || !strings.Contains(result.Text, "response lost") || !strings.Contains(result.Text, "resolve failed") || files.writeCalls != 1 {
		t.Fatalf("resolve failure result = %+v, calls = %d", result, files.writeCalls)
	}
}
