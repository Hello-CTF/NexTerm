package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestUploadFailureEmitsTerminalErrorProgress(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("write failed")
	destination := &failingWriteFS{FileSystem: New(), err: writeErr}
	var progress []Progress
	transferred, err := Upload(context.Background(), source, destination, filepath.Join(dir, "remote"), TransferOptions{
		TaskID: "upload-1",
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	})
	if !errors.Is(err, writeErr) || transferred != 3 {
		t.Fatalf("Upload = %d, %v", transferred, err)
	}
	assertFailedProgress(t, progress, 3, 6)
	if progress[len(progress)-1].TaskID != "upload-1" {
		t.Fatalf("failure task = %#v", progress[len(progress)-1])
	}
	if countDone(progress) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
	}
}

func TestDownloadFailureEmitsTerminalErrorProgress(t *testing.T) {
	readErr := errors.New("read failed")
	source := &staticReaderFS{reader: &failingSizedReader{data: []byte("abc"), size: 6, err: readErr}}
	local := filepath.Join(t.TempDir(), "local")
	var progress []Progress
	transferred, err := Download(context.Background(), source, "remote", local, TransferOptions{
		TaskID: "download-1",
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	})
	if !errors.Is(err, readErr) || transferred != 3 {
		t.Fatalf("Download = %d, %v", transferred, err)
	}
	assertFailedProgress(t, progress, 3, 6)
	if countDone(progress) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", countDone(progress))
	}
}

func TestUploadEarlyFailureEmitsTerminalErrorProgress(t *testing.T) {
	var progress []Progress
	_, err := Upload(context.Background(), filepath.Join(t.TempDir(), "missing"), New(), "remote", TransferOptions{
		TaskID: "upload-2",
		Progress: func(p Progress) {
			progress = append(progress, p)
		},
	})
	if err == nil {
		t.Fatal("missing source upload succeeded")
	}
	assertFailedProgress(t, progress, 0, 0)
}

func TestCanceledTransferEmitsNoProgress(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := Upload(ctx, source, New(), filepath.Join(dir, "remote"), TransferOptions{Progress: func(Progress) { called = true }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload error = %v", err)
	}
	if called {
		t.Fatal("canceled upload reported progress")
	}
}

type failingWriteFS struct {
	*FileSystem
	err error
}

func (f *failingWriteFS) OpenWrite(context.Context, string, bool) (base.RemoteWriter, error) {
	return &errorAfterWrite{err: f.err}, nil
}

type failingSizedReader struct {
	data []byte
	size int64
	err  error
}

func (r *failingSizedReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func (r *failingSizedReader) Size() int64 {
	return r.size
}

func (r *failingSizedReader) Close() error {
	return nil
}

func countDone(progress []Progress) int {
	count := 0
	for _, event := range progress {
		if event.Done {
			count++
		}
	}
	return count
}
