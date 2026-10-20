package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestR1ConfirmedMissingCreateCannotClobberExternalCreate(t *testing.T) {
	files := newFakeFS()
	registry, _ := fileRegistry(files)
	if result := executeTool(registry, "j", "read_file", `{"path":"/new"}`); result.OK {
		t.Fatal("missing read succeeded")
	}
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/new","content":"approved"}`)}
	preparation, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write)
	if err != nil {
		t.Fatal(err)
	}
	files.conditionalCreateHook = func() {
		files.files["/new"] = []byte("external")
	}
	result := registry.Execute(context.Background(), "j", Scope{SessionID: "s"}, write, preparation)
	if result.OK || !strings.Contains(result.Text, ErrFileChanged.Error()) {
		t.Fatalf("result = %+v", result)
	}
	if got := string(files.files["/new"]); got != "external" {
		t.Fatalf("external create was clobbered with %q", got)
	}
}
