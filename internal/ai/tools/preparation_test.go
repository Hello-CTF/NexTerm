package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPreparedWriteUsesItsOwnBaseVersion(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	registry, _ := fileRegistry(files)
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	first := Call{ID: "one", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"one"}`)}
	second := Call{ID: "two", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"two"}`)}
	prepareFirst, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, first)
	if err != nil {
		t.Fatal(err)
	}
	prepareSecond, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, second)
	if err != nil {
		t.Fatal(err)
	}
	if result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, first, prepareFirst); !result.OK {
		t.Fatalf("first write failed: %+v", result)
	}
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, second, prepareSecond)
	if result.OK || !strings.Contains(result.Text, ErrFileChanged.Error()) {
		t.Fatalf("stale preparation was allowed: %+v", result)
	}
	if string(files.files["/a"]) != "one" {
		t.Fatalf("stale write replaced content: %q", files.files["/a"])
	}
}
