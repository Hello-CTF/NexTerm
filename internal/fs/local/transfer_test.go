package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestUploadCreatesMissingRemoteParents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("nested upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinations := map[string]base.FileSystem{
		"mkdir-all":    New(),
		"single-level": &singleLevelMkdirFS{FileSystem: New()},
	}
	for name, filesystem := range destinations {
		t.Run(name, func(t *testing.T) {
			remote := filepath.Join(dir, name, "a", "b", "remote.txt")
			transferred, err := Upload(ctx, source, filesystem, remote, TransferOptions{})
			if err != nil || transferred != int64(len("nested upload")) {
				t.Fatalf("Upload = %d, %v", transferred, err)
			}
			assertBytes(t, remote, []byte("nested upload"))
			sibling := filepath.Join(dir, name, "a", "b", "sibling.txt")
			if _, err := Upload(ctx, source, filesystem, sibling, TransferOptions{Resume: true}); err != nil {
				t.Fatalf("Upload into existing parents = %v", err)
			}
			assertBytes(t, sibling, []byte("nested upload"))
		})
	}
}

func TestUploadFailsWhenRemoteParentComponentIsFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(source, []byte("blocked upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "occupied")
	if err := os.WriteFile(blocker, []byte("a file sits where a directory is needed"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinations := map[string]base.FileSystem{
		"mkdir-all":    New(),
		"single-level": &singleLevelMkdirFS{FileSystem: New()},
	}
	for name, filesystem := range destinations {
		t.Run(name, func(t *testing.T) {
			remote := filepath.Join(blocker, "child", "remote.txt")
			if _, err := Upload(ctx, source, filesystem, remote, TransferOptions{}); err == nil {
				t.Fatal("upload through a file component unexpectedly succeeded")
			}
			if _, err := os.Stat(remote); err == nil {
				t.Fatal("remote target was created through a file component")
			}
		})
	}
}

func TestRemoteParentDir(t *testing.T) {
	cases := map[string]string{
		"/a/b/c.txt": "/a/b",
		"/a":         "/",
		"/":          "",
		"~/a/b":      "~/a",
		"~/a":        "~",
		"~":          "",
		"a/b":        "a",
		"a":          "",
		"file.txt":   "",
		"/a/b/":      "/a",
		`C:\a\b`:     `C:\a`,
		`C:\a`:       `C:\`,
		`C:\`:        "",
	}
	for path, want := range cases {
		if got := parentDir(path); got != want {
			t.Errorf("parentDir(%q) = %q, want %q", path, got, want)
		}
	}
}

// singleLevelMkdirFS mimics sftp Mkdir semantics: one level only, and a bare
// server status error (no fs.ErrExist sentinel) when the directory exists.
type singleLevelMkdirFS struct {
	*FileSystem
}

func (f *singleLevelMkdirFS) Mkdir(_ context.Context, path string) error {
	resolved := real(path)
	if _, err := os.Stat(resolved); err == nil {
		return fmt.Errorf("mkdir %s: server failure", resolved)
	}
	if _, err := os.Stat(filepath.Dir(resolved)); err != nil {
		return fmt.Errorf("mkdir %s: server failure", resolved)
	}
	return os.Mkdir(resolved, 0o755)
}

func TestEnsureRemoteDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	destinations := map[string]base.FileSystem{
		"mkdir-all":    New(),
		"single-level": &singleLevelMkdirFS{FileSystem: New()},
	}
	for name, filesystem := range destinations {
		t.Run(name, func(t *testing.T) {
			nested := filepath.Join(dir, name, "a", "b", "c")
			if err := EnsureRemoteDir(ctx, filesystem, nested); err != nil {
				t.Fatalf("EnsureRemoteDir nested = %v", err)
			}
			if info, err := os.Stat(nested); err != nil || !info.IsDir() {
				t.Fatalf("nested dir stat = %v, %v", info, err)
			}
			if err := EnsureRemoteDir(ctx, filesystem, nested); err != nil {
				t.Fatalf("EnsureRemoteDir existing dir = %v, want a no-op", err)
			}
			blocker := filepath.Join(dir, name+"-blocker")
			if err := os.WriteFile(blocker, []byte("a file sits where a directory is needed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := EnsureRemoteDir(ctx, filesystem, filepath.Join(blocker, "child")); err == nil {
				t.Fatal("a file occupying a parent component unexpectedly succeeded")
			}
			if err := EnsureRemoteDir(ctx, filesystem, blocker); err == nil {
				t.Fatal("a file occupying the target unexpectedly passed as a directory")
			}
			if err := EnsureRemoteDir(ctx, filesystem, ""); err == nil {
				t.Fatal("empty directory path unexpectedly succeeded")
			}
		})
	}
}

func TestUploadResumeAndRestart(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	dir := t.TempDir()
	data := transferData(600_000)
	source := filepath.Join(dir, "source.bin")
	remote := filepath.Join(dir, "remote.bin")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remote, data[:123_456], 0o600); err != nil {
		t.Fatal(err)
	}
	var progress []Progress
	transferred, err := Upload(ctx, source, filesystem, remote, TransferOptions{
		Resume: true,
		TaskID: "upload",
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	})
	if err != nil || transferred != int64(len(data)) {
		t.Fatalf("Upload = %d, %v", transferred, err)
	}
	assertBytes(t, remote, data)
	if len(progress) == 0 || !progress[len(progress)-1].Done || progress[len(progress)-1].Transferred != int64(len(data)) {
		t.Fatalf("final progress = %#v", progress)
	}
	if err := os.WriteFile(remote, bytes.Repeat([]byte("bad"), len(data)+10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Upload(ctx, source, filesystem, remote, TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, remote, data)
}

func TestUploadPreservesRemoteMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not available on Windows")
	}
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.bin")
			remote := filepath.Join(dir, "remote.bin")
			if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(remote, []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(remote, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Upload(context.Background(), source, New(), remote, TransferOptions{}); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(remote)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("remote mode = %v, %v; want %v", info.Mode(), err, mode)
			}
			assertBytes(t, remote, []byte("new"))
		})
	}
}

func TestDownloadResumeAndRestart(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	dir := t.TempDir()
	data := transferData(400_001)
	remote := filepath.Join(dir, "remote.bin")
	local := filepath.Join(dir, "local.bin")
	if err := os.WriteFile(remote, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, data[:200_000], 0o600); err != nil {
		t.Fatal(err)
	}
	transferred, err := Download(ctx, filesystem, remote, local, TransferOptions{Resume: true})
	if err != nil || transferred != int64(len(data)) {
		t.Fatalf("Download = %d, %v", transferred, err)
	}
	assertBytes(t, local, data)
	if err := os.WriteFile(local, bytes.Repeat([]byte("x"), len(data)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Download(ctx, filesystem, remote, local, TransferOptions{}); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, local, data)
}

func TestTransferChunkBoundaries(t *testing.T) {
	ctx := context.Background()
	filesystem := New()
	for _, size := range []int{0, transferChunkSize - 1, transferChunkSize, transferChunkSize + 1} {
		t.Run(stringSize(size), func(t *testing.T) {
			dir := t.TempDir()
			data := transferData(size)
			source := filepath.Join(dir, "source")
			remote := filepath.Join(dir, "remote")
			local := filepath.Join(dir, "local")
			if err := os.WriteFile(source, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Upload(ctx, source, filesystem, remote, TransferOptions{}); err != nil {
				t.Fatal(err)
			}
			assertBytes(t, remote, data)
			if _, err := Download(ctx, filesystem, remote, local, TransferOptions{}); err != nil {
				t.Fatal(err)
			}
			assertBytes(t, local, data)
		})
	}
}

func TestTransferRejectsSameFileAndCancellation(t *testing.T) {
	filesystem := New()
	path := filepath.Join(t.TempDir(), "same")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	decorated := &struct{ *FileSystem }{FileSystem: filesystem}
	targets := []string{path}
	hardlink := path + ".hardlink"
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	targets = append(targets, hardlink)
	for _, target := range targets {
		if _, err := Upload(context.Background(), path, decorated, target, TransferOptions{}); err == nil {
			t.Fatalf("same-file upload to %s unexpectedly succeeded", target)
		}
		if _, err := Download(context.Background(), decorated, target, path, TransferOptions{}); err == nil {
			t.Fatalf("same-file download from %s unexpectedly succeeded", target)
		}
	}
	assertBytes(t, path, []byte("keep"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := Upload(ctx, path, filesystem, path+".copy", TransferOptions{Progress: func(Progress) { called = true }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload error = %v", err)
	}
	if called {
		t.Fatal("canceled upload reported completion")
	}
}

func TestUploadResumeDoesNotOverwriteAfterSizeError(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	remote := filepath.Join(dir, "remote")
	if err := os.WriteFile(source, []byte("new contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remote, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	sizeErr := errors.New("size unavailable")
	destination := &failingSizeFS{FileSystem: New(), err: sizeErr}
	if _, err := Upload(context.Background(), source, destination, remote, TransferOptions{Resume: true}); !errors.Is(err, sizeErr) {
		t.Fatalf("Upload error = %v", err)
	}
	assertBytes(t, remote, []byte("keep"))
}

func TestFinishTransferCountsPartialWriteBeforeError(t *testing.T) {
	writeErr := errors.New("write failed")
	target := &errorAfterWrite{err: writeErr}
	transferred, err := finishTransfer(context.Background(), strings.NewReader("abcdef"), target, 0, 6, TransferOptions{})
	if !errors.Is(err, writeErr) || transferred != 3 {
		t.Fatalf("finishTransfer = %d, %v", transferred, err)
	}
}

func TestVerifyResumeOffset(t *testing.T) {
	filesystem := New()
	path := filepath.Join(t.TempDir(), "offset")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := filesystem.OpenWrite(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := verifyResumeOffset(target, 4); err != nil {
		t.Fatal(err)
	}
	if err := verifyResumeOffset(target, 3); err == nil {
		t.Fatal("mismatched offset unexpectedly succeeded")
	}
}

type errorAfterWrite struct {
	err error
}

func (w *errorAfterWrite) Write(p []byte) (int, error) {
	return 3, w.err
}

func (w *errorAfterWrite) Sync() error {
	return nil
}

func (w *errorAfterWrite) Close() error {
	return nil
}

type failingSizeFS struct {
	*FileSystem
	err error
}

func (f *failingSizeFS) Size(context.Context, string) (int64, error) {
	return 0, f.err
}

func transferData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

func stringSize(size int) string {
	if size == 0 {
		return "empty"
	}
	return strconv.Itoa(size)
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content differs: got %d bytes, want %d", path, len(got), len(want))
	}
}
