package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEditBetweenOneAndTwoMiBRemainsAvailable(t *testing.T) {
	files := newFakeFS()
	files.files["/large"] = []byte(strings.Repeat("a", (1<<20)+1))
	registry, _ := fileRegistry(files)
	result := executeTool(registry, "j", "read_file", `{"path":"/large","max_bytes":2097152}`)
	if !result.OK || !result.Truncated {
		t.Fatalf("read result ok=%v truncated=%v text=%q", result.OK, result.Truncated, result.Text)
	}
	edit := Call{ID: "e", Name: "edit_file", Args: json.RawMessage(`{"path":"/large","old_string":"aa","new_string":"b","replace_all":true}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, edit)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Preview != nil {
		t.Fatal("oversized edit received an unbounded preview")
	}
	result = registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, edit, preparation)
	if !result.OK || result.Change != nil {
		t.Fatalf("edit result=%+v", result)
	}
}
