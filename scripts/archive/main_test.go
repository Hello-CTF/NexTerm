package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, name string, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pkg", "bin", "tool"), "#!/bin/sh\n", 0o700)
	writeFile(t, filepath.Join(root, "pkg", "README.md"), "readme", 0o600)
	writeFile(t, filepath.Join(root, "pkg", "web", "index.html"), "<html>", 0o664)
	return root
}

func readTar(t *testing.T, name string) ([]*tar.Header, map[string]string) {
	t.Helper()
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(compressed)
	var headers []*tar.Header
	contents := map[string]string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return headers, contents
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		headers = append(headers, header)
		contents[header.Name] = string(data)
	}
}

func TestTarIsReproducibleAndNormalized(t *testing.T) {
	root := fixtureTree(t)
	out := t.TempDir()
	first, second := filepath.Join(out, "a.tar.gz"), filepath.Join(out, "b.tar.gz")
	for _, artifact := range []string{first, second} {
		if err := run([]string{"tar", artifact, root, "pkg", "1700000000"}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if !bytes.Equal(a, b) {
		t.Fatal("two archives of the same tree differ")
	}
	// Touching mtimes and ownership-independent metadata must not change the bytes.
	if err := os.Chtimes(filepath.Join(root, "pkg", "README.md"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(out, "c.tar.gz")
	if err := run([]string{"tar", third, root, "pkg", "1700000000"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(third); !bytes.Equal(a, c) {
		t.Fatal("source mtimes leaked into the archive")
	}

	headers, contents := readTar(t, first)
	var names []string
	for _, header := range headers {
		names = append(names, header.Name)
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("%s ownership = %d:%d %q:%q", header.Name, header.Uid, header.Gid, header.Uname, header.Gname)
		}
		if header.ModTime.Unix() != 1700000000 {
			t.Fatalf("%s mtime = %v", header.Name, header.ModTime)
		}
	}
	want := []string{"pkg/", "pkg/README.md", "pkg/bin/", "pkg/bin/tool", "pkg/web/", "pkg/web/index.html"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("members = %v, want %v", names, want)
	}
	modes := map[string]int64{}
	for _, header := range headers {
		modes[header.Name] = header.Mode
	}
	if modes["pkg/"] != 0o755 || modes["pkg/bin/tool"] != 0o755 || modes["pkg/README.md"] != 0o644 || modes["pkg/web/index.html"] != 0o644 {
		t.Fatalf("modes = %v", modes)
	}
	if contents["pkg/bin/tool"] != "#!/bin/sh\n" {
		t.Fatalf("content = %q", contents["pkg/bin/tool"])
	}
	if info, _ := os.Stat(first); info.Mode().Perm() != 0o644 {
		t.Fatalf("artifact mode = %o", info.Mode().Perm())
	}
	listed, err := listArchive(first)
	if err != nil || !reflect.DeepEqual(listed, want) {
		t.Fatalf("list = %v, %v", listed, err)
	}
}

func TestTarAcceptsASingleFileMember(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "nexterm-agent"), "agent", 0o755)
	artifact := filepath.Join(t.TempDir(), "agent.tar.gz")
	if err := run([]string{"tar", artifact, root, "nexterm-agent", "1"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	headers, _ := readTar(t, artifact)
	if len(headers) != 1 || headers[0].Name != "nexterm-agent" || headers[0].Mode != 0o755 {
		t.Fatalf("headers = %+v", headers)
	}
}

func TestTarRejectsSymlinks(t *testing.T) {
	root := fixtureTree(t)
	if err := os.Symlink("README.md", filepath.Join(root, "pkg", "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	err := run([]string{"tar", filepath.Join(t.TempDir(), "x.tar.gz"), root, "pkg", "1"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "regular file or directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestZipStoresOneExecutableWithDOSTimeOnly(t *testing.T) {
	source := filepath.Join(t.TempDir(), "nexterm-agent.exe")
	writeFile(t, source, "MZ agent", 0o644)
	out := t.TempDir()
	first, second := filepath.Join(out, "a.zip"), filepath.Join(out, "b.zip")
	for _, artifact := range []string{first, second} {
		if err := run([]string{"zip", artifact, source, "nexterm-agent.exe", "1700000000"}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if !bytes.Equal(a, b) {
		t.Fatal("two zips of the same file differ")
	}
	reader, err := zip.OpenReader(first)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 1 {
		t.Fatalf("entries = %d", len(reader.File))
	}
	entry := reader.File[0]
	if entry.Name != "nexterm-agent.exe" || entry.Mode().Perm() != 0o755 || len(entry.Extra) != 0 || entry.Method != zip.Deflate {
		t.Fatalf("entry = %s mode=%o extra=%d method=%d", entry.Name, entry.Mode().Perm(), len(entry.Extra), entry.Method)
	}
	if got := entry.Modified.UTC(); !got.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Fatalf("modified = %v", got)
	}

	extracted := t.TempDir()
	if err := run([]string{"extract", first, extracted}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(extracted, "nexterm-agent.exe")); err != nil || string(data) != "MZ agent" {
		t.Fatalf("extracted = %q, %v", data, err)
	}
	var listed bytes.Buffer
	if err := run([]string{"list", first}, &listed); err != nil || listed.String() != "nexterm-agent.exe\n" {
		t.Fatalf("list = %q, %v", listed.String(), err)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "evil.zip")
	file, err := os.Create(artifact)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte("x"))
	writer.Close()
	file.Close()
	destination := t.TempDir()
	if err := run([]string{"extract", artifact, destination}, io.Discard); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "escape")); err == nil {
		t.Fatal("traversal entry was written")
	}
}

func TestCopyTreeNormalizesModesAndRejectsLinks(t *testing.T) {
	root := fixtureTree(t)
	destination := filepath.Join(t.TempDir(), "web")
	if err := run([]string{"copy-tree", filepath.Join(root, "pkg"), destination}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{"": 0o755, "bin": 0o755, "bin/tool": 0o644, "README.md": 0o644} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, %v; want %o", name, info, err, want)
		}
	}
	if err := os.Symlink("README.md", filepath.Join(root, "pkg", "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	err := run([]string{"copy-tree", filepath.Join(root, "pkg"), t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectsUnknownUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"tar"}, {"zip", "a", "b", "c", "-1"}, {"nope"}} {
		if err := run(args, io.Discard); err == nil {
			t.Fatalf("args %v were accepted", args)
		}
	}
}
