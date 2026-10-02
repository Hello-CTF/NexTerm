package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestWhitespaceAliasDoesNotShareReadAuthorization(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("target")
	files.files[" /a "] = []byte("different file")
	registry, _ := fileRegistry(files)
	if result := executeTool(registry, "j", "read_file", `{"path":" /a "}`); !result.OK {
		t.Fatal(result.Text)
	}
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"overwrite"}`)}
	if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write); !errors.Is(err, ErrReadRequired) {
		t.Fatalf("whitespace alias shared read authorization: %v", err)
	}
}
