package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/fs/conditional"
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
	if !strings.Contains(einoDescriptions["write_file"], "no-clobber") || !strings.Contains(einoDescriptions["write_file"], "非事务覆盖") {
		t.Fatalf("write_file description = %q", einoDescriptions["write_file"])
	}
	if !strings.Contains(einoDescriptions["edit_file"], "非事务覆盖") {
		t.Fatalf("edit_file description = %q", einoDescriptions["edit_file"])
	}
}
