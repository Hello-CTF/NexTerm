//go:build windows

package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"golang.org/x/sys/windows"
)

func TestWindowsListClassifiesJunctionsAndHiddenAttributes(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, output)
	}
	hidden := filepath.Join(root, "hidden.txt")
	if err := os.WriteFile(hidden, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(hidden)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(name, windows.FILE_ATTRIBUTE_HIDDEN); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".dotfile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := New().List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]base.FileEntry{}
	for _, entry := range entries {
		got[entry.Name] = entry
	}
	if entry := got["junction"]; entry.Kind != base.FileSymlink || entry.TargetKind != base.FileDirectory || entry.SymlinkTarget == "" {
		t.Fatalf("junction = %+v, want a symlink to a directory with its target", entry)
	}
	if entry := got["target"]; entry.Kind != base.FileDirectory || entry.Hidden {
		t.Fatalf("target = %+v", entry)
	}
	if entry := got["hidden.txt"]; entry.Kind != base.FileFile || !entry.Hidden {
		t.Fatalf("hidden.txt = %+v, want a hidden file", entry)
	}
	if entry := got[".dotfile"]; entry.Hidden {
		t.Fatalf(".dotfile = %+v, want visible: Windows hides by attribute, not by name", entry)
	}
	junctionEntries, err := New().List(context.Background(), junction)
	if err != nil || len(junctionEntries) != 0 {
		t.Fatalf("browse junction = %+v, %v", junctionEntries, err)
	}

	roots, err := New().Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (base.FileCapabilities{Chmod: base.ChmodReadOnly, Pack: true, Extract: true, CaseInsensitive: true}); roots.Capabilities != want {
		t.Fatalf("capabilities = %+v, want %+v", roots.Capabilities, want)
	}
}
