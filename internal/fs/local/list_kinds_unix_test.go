//go:build !windows

package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestListResolvesSymlinkTargetKindsAndDotHidden(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".secret"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"to-dir": "dir", "to-file": "file", "broken": "missing"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := New().List(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]base.FileEntry{}
	for _, entry := range entries {
		got[entry.Name] = entry
	}
	for name, want := range map[string]struct {
		kind, target base.FileKind
		link         string
	}{
		"to-dir":  {base.FileSymlink, base.FileDirectory, "dir"},
		"to-file": {base.FileSymlink, base.FileFile, "file"},
		"broken":  {base.FileSymlink, "", "missing"},
		"dir":     {base.FileDirectory, "", ""},
		"file":    {base.FileFile, "", ""},
	} {
		entry := got[name]
		if entry.Kind != want.kind || entry.TargetKind != want.target || entry.SymlinkTarget != want.link || entry.Hidden {
			t.Fatalf("%s = %+v, want kind %q target %q link %q visible", name, entry, want.kind, want.target, want.link)
		}
	}
	if !got[".secret"].Hidden {
		t.Fatalf(".secret is not hidden: %+v", got[".secret"])
	}
}

func TestRootsReportLocalCapabilities(t *testing.T) {
	roots, err := New().Roots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := base.FileCapabilities{Chmod: base.ChmodPOSIX, Pack: true, CaseInsensitive: runtime.GOOS == "darwin" || runtime.GOOS == "ios"}
	if roots.Capabilities != want {
		t.Fatalf("capabilities = %+v, want %+v", roots.Capabilities, want)
	}
}
