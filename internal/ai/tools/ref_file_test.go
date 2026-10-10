package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReadRefFileGuards(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("hello")
	files.files["/bin"] = []byte{0xff, 0xfe, 'a'}
	registry, _ := fileRegistry(files)

	content, err := registry.ReadRefFile(context.Background(), Scope{SessionID: "s"}, "/a")
	if err != nil || content != "hello" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	content, err = registry.ReadRefFile(context.Background(), Scope{SessionID: "s"}, "/bin")
	if err != nil || !strings.Contains(content, "�") {
		t.Fatalf("invalid utf8 must be replaced: %q %v", content, err)
	}
	if _, err := registry.ReadRefFile(context.Background(), Scope{SessionID: "s"}, "  "); err == nil {
		t.Fatal("empty path accepted")
	}
	if _, err := registry.ReadRefFile(context.Background(), Scope{SessionID: "s"}, "/missing"); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := registry.ReadRefFile(context.Background(), Scope{}, "/a"); err == nil {
		t.Fatal("scope without session accepted")
	}
}

func TestReadRefFileDoesNotRememberVersion(t *testing.T) {
	files := newFakeFS()
	files.files["/a"] = []byte("old")
	registry, _ := fileRegistry(files)
	if _, err := registry.ReadRefFile(context.Background(), Scope{SessionID: "s"}, "/a"); err != nil {
		t.Fatal(err)
	}
	write := Call{ID: "w", Name: "write_file", Args: json.RawMessage(`{"path":"/a","content":"new"}`)}
	if _, err := registry.Prepare(context.Background(), "j", Scope{SessionID: "s"}, write); !errors.Is(err, ErrReadRequired) {
		t.Fatalf("ref read must not authorize writes: %v", err)
	}
}
