package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
