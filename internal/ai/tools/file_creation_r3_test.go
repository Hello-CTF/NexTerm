package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestR3DeletedExistingUsesAtomicCreate(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	registry, _ := fileRegistry(files)
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	delete(files.files, "/a")
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
	if !result.OK || !strings.Contains(result.Text, "已创建") || !strings.Contains(result.Text, "原子 no-clobber") {
		t.Fatalf("deleted-existing create result = %+v", result)
	}
	if _, ok := files.files["/a.nexterm-bak"]; ok {
		t.Fatal("deleted-existing creation incorrectly wrote a backup")
	}
}

func TestR3DeletedExistingExternalRecreateCannotBeClobbered(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	registry, _ := fileRegistry(files)
	executeTool(registry, "j", "read_file", `{"path":"/a"}`)
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"approved"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	delete(files.files, "/a")
	files.conditionalCreateHook = func() { files.files["/a"] = []byte("external") }
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
	if result.OK || !strings.Contains(result.Text, ErrFileChanged.Error()) {
		t.Fatalf("external recreate result = %+v", result)
	}
	if got := string(files.files["/a"]); got != "external" {
		t.Fatalf("external recreate was clobbered with %q", got)
	}
}
