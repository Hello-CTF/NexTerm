package diagcapture

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform"
)

func TestShellCommandPerPlatform(t *testing.T) {
	cases := []struct {
		goos   string
		reveal bool
		target string
		args   []string
	}{
		{"darwin", false, "/r", []string{"open", "/r"}},
		{"darwin", true, "/r/a.zip", []string{"open", "-R", "/r/a.zip"}},
		{"windows", false, `C:\r`, []string{"explorer", `C:\r`}},
		{"windows", true, `C:\r\a.zip`, []string{"explorer", `/select,C:\r\a.zip`}},
		{"linux", false, "/r", []string{"xdg-open", "/r"}},
		{"linux", true, "/r/a.zip", []string{"xdg-open", filepath.Dir("/r/a.zip")}},
	}
	for _, c := range cases {
		cmd := shellCommand(c.goos, c.target, c.reveal)
		if cmd == nil {
			t.Fatalf("%s reveal=%v: no command", c.goos, c.reveal)
		}
		got := append([]string{filepath.Base(cmd.Args[0])}, cmd.Args[1:]...)
		if len(got) != len(c.args) {
			t.Fatalf("%s reveal=%v: %v", c.goos, c.reveal, got)
		}
		for i := range got {
			if got[i] != c.args[i] {
				t.Fatalf("%s reveal=%v: got %v want %v", c.goos, c.reveal, got, c.args)
			}
		}
	}
	if shellCommand("plan9", "/r", false) != nil {
		t.Fatal("unsupported platform must have no command")
	}
}

func TestResolveArchiveStaysUnderTheRoot(t *testing.T) {
	paths := platform.NewPaths(t.TempDir())
	root := Root(paths)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(root, "a.zip")
	write := func(p string) {
		t.Helper()
		if err := os.WriteFile(p, []byte("zip"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(good)
	if got, err := ResolveArchive(paths, good); err != nil || got != good {
		t.Fatalf("valid archive: %q %v", got, err)
	}

	outside := filepath.Join(t.TempDir(), "x.zip")
	write(outside)
	notZip := filepath.Join(root, "a.txt")
	write(notZip)
	nested := filepath.Join(root, "sub", "n.zip")
	if err := os.MkdirAll(filepath.Dir(nested), 0o700); err != nil {
		t.Fatal(err)
	}
	write(nested)
	dirZip := filepath.Join(root, "d.zip")
	if err := os.Mkdir(dirZip, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []string{"", outside, notZip, nested, dirZip, filepath.Join(root, "missing.zip"), filepath.Join(root, "..", "a.zip")}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link.zip")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		bad = append(bad, link)
	}
	for _, p := range bad {
		if got, err := ResolveArchive(paths, p); err == nil {
			t.Errorf("ResolveArchive(%q) accepted: %q", p, got)
		}
	}
}
