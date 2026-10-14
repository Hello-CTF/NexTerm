package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyDroppedPaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pics")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "gone.txt")

	paths := classifyDroppedPaths([]string{file, dir, missing})
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(paths))
	}
	if paths[0] != (nativeDroppedPath{Path: file, Dir: false}) {
		t.Errorf("file misclassified: %+v", paths[0])
	}
	if paths[1] != (nativeDroppedPath{Path: dir, Dir: true}) {
		t.Errorf("dir misclassified: %+v", paths[1])
	}
	if paths[2] != (nativeDroppedPath{Path: missing, Dir: false}) {
		t.Errorf("missing path should stay a file: %+v", paths[2])
	}
}

func TestClassifyDroppedPathsEmpty(t *testing.T) {
	if paths := classifyDroppedPaths(nil); len(paths) != 0 {
		t.Fatalf("expected no paths, got %+v", paths)
	}
}
