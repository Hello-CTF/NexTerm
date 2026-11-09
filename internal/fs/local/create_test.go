package local

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateFileIsExclusive(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	root := t.TempDir()
	created := filepath.Join(root, "new.conf")
	if err := filesystem.CreateFile(ctx, created); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(created); err != nil || len(data) != 0 {
		t.Fatalf("created file = %q, %v; want an empty file", data, err)
	}
	existing := filepath.Join(root, "nginx.conf")
	if err := os.WriteFile(existing, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.CreateFile(ctx, existing); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile over an existing file = %v, want fs.ErrExist", err)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "keep me" {
		t.Fatalf("existing file after refused create = %q, %v", data, err)
	}
	if err := filesystem.CreateFile(ctx, root); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile over a directory = %v, want fs.ErrExist", err)
	}
	if err := filesystem.CreateFile(ctx, filepath.Join(root, "missing", "child")); err == nil || errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile under a missing parent = %v, want an error that is not fs.ErrExist", err)
	}
}
