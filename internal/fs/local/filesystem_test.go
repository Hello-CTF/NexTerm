package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestFileSystemMetadataAndRecursiveOperations(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	root := filepath.Join(t.TempDir(), "本机 Ω")
	if err := filesystem.Mkdir(ctx, filepath.Join(root, "nested", "deep")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "你好-🚀.txt")
	if err := filesystem.WriteFile(ctx, file, []byte("hello"), false); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Mkdir(ctx, filepath.Join(root, "a-dir")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(file, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := filesystem.List(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 || entries[0].Kind != base.FileDirectory || entries[1].Kind != base.FileDirectory {
		t.Fatalf("directories must be first: %#v", entries)
	}
	var found *base.FileEntry
	for i := range entries {
		if entries[i].Path == file {
			found = &entries[i]
		}
		if entries[i].Name == "link" && entries[i].Kind != base.FileSymlink {
			t.Fatalf("symlink kind = %q", entries[i].Kind)
		}
	}
	if found == nil || found.Kind != base.FileFile || found.Size != 5 || found.Name != "你好-🚀.txt" || found.Mode == 0 || found.ModTime.IsZero() {
		t.Fatalf("unexpected Unicode file metadata: %#v", found)
	}
	exists, err := filesystem.Exists(ctx, file)
	if err != nil || !exists {
		t.Fatalf("Exists = %v, %v", exists, err)
	}
	size, err := filesystem.Size(ctx, file)
	if err != nil || size != 5 {
		t.Fatalf("Size = %d, %v", size, err)
	}
	renamed := filepath.Join(root, "nested", "renamed.txt")
	if err := filesystem.Rename(ctx, file, renamed); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.Delete(ctx, filepath.Join(root, "nested"), true); err != nil {
		t.Fatal(err)
	}
	exists, err = filesystem.Exists(ctx, renamed)
	if err != nil || exists {
		t.Fatalf("recursive delete left target: %v, %v", exists, err)
	}
}

func TestReadFileLimitAndPlatformErrors(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		maximum int64
		want    string
		fail    bool
	}{{4, "", true}, {5, "12345", false}, {6, "12345", false}} {
		got, err := filesystem.ReadFile(ctx, path, test.maximum)
		if test.fail && err == nil {
			t.Fatalf("limit %d unexpectedly succeeded", test.maximum)
		}
		if !test.fail && (err != nil || string(got) != test.want) {
			t.Fatalf("limit %d = %q, %v", test.maximum, got, err)
		}
	}
	if _, err := filesystem.ReadFile(ctx, path, -1); err == nil {
		t.Fatal("negative limit unexpectedly succeeded")
	}
	if _, err := filesystem.ReadFile(ctx, filepath.Join(filepath.Dir(path), "missing"), 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
	if err := filesystem.Delete(ctx, filepath.Join(filepath.Dir(path), "missing"), false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing delete error = %v", err)
	}
}

func TestWriteFileAtomicBackupAndFailure(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := filesystem.WriteFile(ctx, path, []byte("old"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + BackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file created a backup: %v", err)
	}
	if err := filesystem.WriteFile(ctx, path, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path+BackupSuffix, "old")
	if err := filesystem.WriteFile(ctx, path, []byte("third"), true); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path+BackupSuffix, "new")
	if err := filesystem.WriteFile(ctx, path, []byte("without-backup"), false); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path+BackupSuffix, "new")
	if err := os.Remove(path + BackupSuffix); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+BackupSuffix, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := filesystem.WriteFile(ctx, path, []byte("must-not-commit"), true); err == nil {
		t.Fatal("backup failure unexpectedly succeeded")
	}
	assertFileContent(t, path, "without-backup")
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.nexterm-tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestChecksumAndStreams(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	path := filepath.Join(t.TempDir(), "abc.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	for algorithm, want := range map[string]string{
		"MD5":     "900150983cd24fb0d6963f7d28e17f72",
		"sha-256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	} {
		got, err := filesystem.Checksum(ctx, path, algorithm)
		if err != nil || got != want {
			t.Fatalf("Checksum(%s) = %q, %v", algorithm, got, err)
		}
	}
	if _, err := filesystem.Checksum(ctx, path, "crc32"); err == nil {
		t.Fatal("unsupported algorithm unexpectedly succeeded")
	}
	target, err := filesystem.OpenWrite(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Write([]byte("def")); err != nil {
		t.Fatal(err)
	}
	if err := target.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := filesystem.OpenRead(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if source.Size() != 6 {
		t.Fatalf("stream size = %d", source.Size())
	}
	got, err := io.ReadAll(source)
	if err != nil || string(got) != "abcdef" {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	target, err = filesystem.OpenWrite(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "z")
}

func TestHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := real("~"); got != home {
		t.Fatalf("real(~) = %q", got)
	}
	if got := real("~/child"); got != filepath.Join(home, "child") {
		t.Fatalf("real(~/child) = %q", got)
	}
	if got := real(`~\child`); got != filepath.Join(home, "child") {
		t.Fatalf("real(~\\child) = %q", got)
	}
	if got := real("~other/file"); got != "~other/file" {
		t.Fatalf("real(~other/file) = %q", got)
	}
	if got := real("relative"); got != "relative" {
		t.Fatalf("real(relative) = %q", got)
	}
}

func TestReadBinaryWithoutUTF8Conversion(t *testing.T) {
	filesystem := New()
	path := filepath.Join(t.TempDir(), "binary")
	want := []byte{0, 0xff, 0xfe, 1, 2}
	if err := filesystem.WriteFile(context.Background(), path, want, false); err != nil {
		t.Fatal(err)
	}
	got, err := filesystem.ReadFile(context.Background(), path, int64(len(want)))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("binary read = %v, %v", got, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Compare(string(got), want) != 0 {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
